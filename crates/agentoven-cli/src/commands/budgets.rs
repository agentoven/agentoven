//! `agentoven budgets` — the kitchen's period budgets (Pro, ADR-0051).
//!
//! A budget caps what the kitchen, or one provider, agent or API key in it, may spend in a day, a
//! week or a month, in dollars or tokens. Creating one or lowering it is not a decision. Raising it
//! (or removing it) is a signed one, like every human decision: add `--sign`. `--output json` prints
//! the server's JSON as it is.

use agentoven_core::{AgentOvenClient, BudgetScope, Signature};
use clap::{Args, Subcommand};
use colored::Colorize;
use serde_json::Value;

use super::signing::{clip, pretty, signing_hint, SignArgs};

#[derive(Subcommand)]
pub enum BudgetsCommands {
    /// The kitchen's budgets with what is left (cost:read).
    #[command(visible_alias = "ls")]
    List,
    /// Create or change a budget (budget:extend). Raising one is signed: add --sign.
    Set(SetArgs),
    /// Remove a budget (budget:extend). It lifts the limit, so it is signed: add --sign.
    #[command(visible_alias = "remove")]
    Rm(RmArgs),
    /// The ledger, newest first: one line per model call per budget it was charged to.
    Ledger(LedgerArgs),
    /// The thresholds the budgets have crossed, and whether the notification channels were told.
    Alerts(AlertsArgs),
    /// What the kitchen's calls amounted to, ranked: by agent, model, provider or day, with the response cache's savings.
    Usage(UsageArgs),
}

/// Which budget. With none of the three it is the whole kitchen's.
#[derive(Args, Clone, Default)]
pub struct ScopeArgs {
    /// Count only this provider's calls.
    #[arg(long, conflicts_with_all = ["agent", "key"])]
    pub provider: Option<String>,
    /// Count only this agent's calls.
    #[arg(long, conflicts_with_all = ["provider", "key"])]
    pub agent: Option<String>,
    /// Count only calls made with this credential (its subject, e.g. scopedkey:<id>; not --api-key, which is the key this command signs in with).
    #[arg(long, conflicts_with_all = ["provider", "agent"])]
    pub key: Option<String>,
    /// What the budget is measured in.
    #[arg(long, default_value = "usd", value_parser = ["usd", "tokens"])]
    pub unit: String,
}

impl ScopeArgs {
    fn scope(&self) -> BudgetScope<'_> {
        BudgetScope {
            provider: self.provider.as_deref(),
            agent: self.agent.as_deref(),
            api_key: self.key.as_deref(),
            unit: Some(&self.unit),
        }
    }
}

#[derive(Args)]
pub struct SetArgs {
    /// The amount, in --unit (dollars, or a whole number of tokens).
    pub amount: f64,
    #[command(flatten)]
    pub scope: ScopeArgs,
    /// The period it resets on (monthly when left out).
    #[arg(long, value_parser = ["daily", "weekly", "monthly", "lifetime"])]
    pub period: Option<String>,
    /// Tell people at this percentage (repeatable). With --alert or --block the defaults (50, 80 and a stop at 100) are replaced.
    #[arg(long = "alert", value_name = "PCT")]
    pub alert: Vec<u32>,
    /// Stop calls at this percentage (the lowest is where they stop).
    #[arg(long = "block", value_name = "PCT")]
    pub block: Vec<u32>,
    #[command(flatten)]
    pub signing: SignArgs,
}

#[derive(Args)]
pub struct RmArgs {
    #[command(flatten)]
    pub scope: ScopeArgs,
    #[command(flatten)]
    pub signing: SignArgs,
}

#[derive(Args)]
pub struct LedgerArgs {
    /// Only this budget's entries.
    #[arg(long)]
    pub budget_id: Option<String>,
    /// Only entries since this RFC 3339 time.
    #[arg(long)]
    pub since: Option<String>,
    /// Page: entries with an id below this.
    #[arg(long)]
    pub before: Option<u64>,
    #[arg(long)]
    pub limit: Option<u32>,
}

#[derive(Args)]
pub struct AlertsArgs {
    #[arg(long)]
    pub limit: Option<u32>,
}

#[derive(Args)]
pub struct UsageArgs {
    /// How to group the calls.
    #[arg(long, default_value = "agent", value_parser = ["agent", "model", "provider", "day"])]
    pub by: String,
    /// The last N days (30 when left out).
    #[arg(long)]
    pub days: Option<u32>,
    #[arg(long)]
    pub limit: Option<u32>,
}

pub async fn execute(
    cmd: BudgetsCommands,
    client: AgentOvenClient,
    json: bool,
) -> anyhow::Result<()> {
    match run(cmd, &client, json).await {
        Ok(out) => {
            print!("{out}");
            Ok(())
        }
        Err(e) => {
            if let Some(h) = refusal_hint(&e).or_else(|| signing_hint(&e)) {
                eprintln!("{h}");
            }
            Err(e)
        }
    }
}

/// What to tell the person about a refusal they can act on: an unsigned raise, a rate limit.
pub fn refusal_hint(e: &anyhow::Error) -> Option<String> {
    let api = e.downcast_ref::<agentoven_core::ApiError>()?;
    if api.code == "signature_required" {
        return Some(format!(
            "{} Raising or removing a budget is a signed decision: add --sign (a local account's password), or --signing-token.",
            "✗".red().bold()
        ));
    }
    if api.is_rate_limited() {
        return Some(format!(
            "{} Rate limited: try again in {}s.",
            "✗".red().bold(),
            api.retry_after.unwrap_or(1)
        ));
    }
    None
}

pub async fn run(
    cmd: BudgetsCommands,
    client: &AgentOvenClient,
    json: bool,
) -> anyhow::Result<String> {
    match cmd {
        BudgetsCommands::List => {
            let bs = client.list_budgets().await?;
            if json {
                return Ok(pretty(&Value::Array(bs)) + "\n");
            }
            Ok(budget_table(&bs))
        }
        BudgetsCommands::Set(a) => {
            let thresholds = thresholds(&a.alert, &a.block)?;
            let signing = a.signing.resolve()?;
            let b = client
                .set_budget(
                    &a.scope.scope(),
                    a.amount,
                    a.period.as_deref(),
                    thresholds.as_deref(),
                    signing.signature(),
                )
                .await?;
            if json {
                return Ok(pretty(&b) + "\n");
            }
            let signed = if matches!(signing.signature(), Signature::None) {
                ""
            } else {
                " (signed)"
            };
            Ok(format!(
                "\n  {} Budget for {}: {}, {}{signed}.\n",
                "✓".green().bold(),
                who(&b).bold(),
                amount(&b["unit"], b["amount"].as_f64().unwrap_or(0.0)),
                b["period"].as_str().unwrap_or("-")
            ))
        }
        BudgetsCommands::Rm(a) => {
            let signing = a.signing.resolve()?;
            let r = client
                .remove_budget(&a.scope.scope(), signing.signature())
                .await?;
            if json {
                return Ok(pretty(&r) + "\n");
            }
            let signed = if matches!(signing.signature(), Signature::None) {
                ""
            } else {
                " (signed)"
            };
            Ok(format!(
                "\n  {} Budget removed{signed}. Its ledger entries stay.\n",
                "✓".green().bold()
            ))
        }
        BudgetsCommands::Ledger(a) => {
            let es = client
                .list_budget_ledger(
                    a.budget_id.as_deref(),
                    a.since.as_deref(),
                    a.before,
                    a.limit,
                )
                .await?;
            if json {
                return Ok(pretty(&Value::Array(es)) + "\n");
            }
            if es.is_empty() {
                return Ok("\n  (no entries)\n".into());
            }
            let mut out = format!(
                "\n  {:<8} {:<22} {:<16} {:<22} {:>9} {:>10}\n",
                "ID".bold(),
                "AT".bold(),
                "AGENT".bold(),
                "MODEL".bold(),
                "TOKENS".bold(),
                "COST".bold()
            );
            for e in &es {
                out.push_str(&format!(
                    "  {:<8} {:<22} {:<16} {:<22} {:>9} {:>10}\n",
                    e["id"],
                    clip(e["at"].as_str().unwrap_or("-"), 20),
                    clip(e["agent"].as_str().unwrap_or("-"), 15),
                    clip(e["model"].as_str().unwrap_or("-"), 21),
                    e["input_tokens"].as_i64().unwrap_or(0)
                        + e["output_tokens"].as_i64().unwrap_or(0),
                    format!("${:.4}", e["cost_usd"].as_f64().unwrap_or(0.0)),
                ));
            }
            Ok(out)
        }
        BudgetsCommands::Alerts(a) => {
            let al = client.list_budget_alerts(a.limit).await?;
            if json {
                return Ok(pretty(&Value::Array(al)) + "\n");
            }
            if al.is_empty() {
                return Ok("\n  (no thresholds crossed)\n".into());
            }
            let mut out = format!(
                "\n  {:<22} {:<28} {:>5} {:<7} {:<22} {}\n",
                "AT".bold(),
                "BUDGET".bold(),
                "PCT".bold(),
                "ACTION".bold(),
                "SPENT".bold(),
                "TOLD".bold()
            );
            for x in &al {
                out.push_str(&format!(
                    "  {:<22} {:<28} {:>5} {:<7} {:<22} {}\n",
                    clip(x["fired_at"].as_str().unwrap_or("-"), 20),
                    clip(&who(x), 27),
                    x["threshold_pct"],
                    x["action"].as_str().unwrap_or("-"),
                    format!(
                        "{} of {}",
                        amount(&x["unit"], x["consumed"].as_f64().unwrap_or(0.0)),
                        amount(&x["unit"], x["amount"].as_f64().unwrap_or(0.0))
                    ),
                    if x["notified"].as_bool().unwrap_or(false) {
                        "yes"
                    } else {
                        "no (throttled or no channel)"
                    },
                ));
            }
            Ok(out)
        }
        BudgetsCommands::Usage(a) => {
            let rows = client.budget_usage(&a.by, a.days, a.limit).await?;
            if json {
                return Ok(pretty(&Value::Array(rows)) + "\n");
            }
            if rows.is_empty() {
                return Ok("\n  (no usage yet; the meter writes every few seconds)\n".into());
            }
            let mut out = format!(
                "\n  {:<26} {:>8} {:>12} {:>10} {:>8} {:>10}\n",
                a.by.to_uppercase().bold(),
                "CALLS".bold(),
                "TOKENS".bold(),
                "COST".bold(),
                "CACHE%".bold(),
                "SAVED".bold()
            );
            for r in &rows {
                let (h, m) = (
                    r["cache_hits"].as_i64().unwrap_or(0),
                    r["cache_misses"].as_i64().unwrap_or(0),
                );
                let rate = if h + m > 0 {
                    format!("{:.0}%", h as f64 * 100.0 / (h + m) as f64)
                } else {
                    "-".into()
                };
                out.push_str(&format!(
                    "  {:<26} {:>8} {:>12} {:>10} {:>8} {:>10}\n",
                    clip(r["group"].as_str().unwrap_or("(none)"), 25),
                    r["calls"],
                    r["input_tokens"].as_i64().unwrap_or(0)
                        + r["output_tokens"].as_i64().unwrap_or(0),
                    format!("${:.4}", r["cost_usd"].as_f64().unwrap_or(0.0)),
                    rate,
                    format!("${:.4}", r["cache_saved_usd"].as_f64().unwrap_or(0.0)),
                ));
            }
            Ok(out)
        }
    }
}

/// `--alert` and `--block` as the server's thresholds, or `None` to take its defaults.
fn thresholds(alerts: &[u32], blocks: &[u32]) -> anyhow::Result<Option<Vec<(u32, &'static str)>>> {
    if alerts.is_empty() && blocks.is_empty() {
        return Ok(None);
    }
    let mut t: Vec<(u32, &'static str)> = alerts
        .iter()
        .map(|p| (*p, "alert"))
        .chain(blocks.iter().map(|p| (*p, "block")))
        .collect();
    if t.iter().any(|(p, _)| *p == 0) {
        anyhow::bail!("a threshold is a percentage above 0");
    }
    t.sort();
    Ok(Some(t))
}

/// An amount in its unit.
fn amount(unit: &Value, v: f64) -> String {
    if unit.as_str() == Some("tokens") {
        format!("{v:.0} tokens")
    } else {
        format!("${v:.4}")
    }
}

/// Whose budget, in words.
fn who(b: &Value) -> String {
    for (k, label) in [
        ("provider", "provider"),
        ("agent", "agent"),
        ("api_key", "key"),
    ] {
        if let Some(n) = b[k].as_str().filter(|n| !n.is_empty()) {
            return format!("{label} {n}");
        }
    }
    "the kitchen".into()
}

fn budget_table(bs: &[Value]) -> String {
    if bs.is_empty() {
        return "\n  (no budgets: the kitchen spends without a period cap)\n".into();
    }
    let mut out = format!(
        "\n  {:<28} {:<8} {:<9} {:>16} {:>16} {:>6}  {}\n",
        "BUDGET".bold(),
        "UNIT".bold(),
        "PERIOD".bold(),
        "USED".bold(),
        "AMOUNT".bold(),
        "%".bold(),
        "RESETS".bold()
    );
    for b in bs {
        let unit = &b["unit"];
        let state = if b["exhausted"].as_bool().unwrap_or(false) {
            " EXHAUSTED".red().bold().to_string()
        } else {
            String::new()
        };
        out.push_str(&format!(
            "  {:<28} {:<8} {:<9} {:>16} {:>16} {:>6}  {}{state}\n",
            clip(&who(b), 27),
            unit.as_str().unwrap_or("usd"),
            b["period"].as_str().unwrap_or("-"),
            amount(unit, b["consumed"].as_f64().unwrap_or(0.0)),
            amount(unit, b["amount"].as_f64().unwrap_or(0.0)),
            format!("{:.0}", b["used_pct"].as_f64().unwrap_or(0.0)),
            clip(b["reset_at"].as_str().unwrap_or("never"), 20),
        ));
    }
    out
}

#[cfg(test)]
mod tests {
    use super::*;
    use agentoven_core::testing::{fake_control_plane, json as reply, Routes};
    use serde_json::json;

    async fn client() -> (AgentOvenClient, agentoven_core::testing::Log) {
        let routes = Routes::from([
            (
                "POST /api/v1/signatures/challenge".to_string(),
                reply(
                    200,
                    json!({"subject": {"digest": "d"}, "reauth": "password"}),
                ),
            ),
            (
                "POST /api/v1/signatures/token".to_string(),
                reply(200, json!({"signing_token": "tok.sig"})),
            ),
            (
                "GET /api/v1/spend/budgets".to_string(),
                reply(
                    200,
                    json!({"budgets": [
                {"scope": "kitchen", "unit": "usd", "amount": 100.0, "consumed": 42.5, "used_pct": 42.5, "period": "monthly", "reset_at": "2026-12-01T00:00:00Z"},
                {"scope": "agent", "agent": "triage", "unit": "tokens", "amount": 5000.0, "consumed": 5000.0, "used_pct": 100.0, "period": "daily", "exhausted": true}]}),
                ),
            ),
            (
                "PUT /api/v1/spend/budgets".to_string(),
                reply(
                    200,
                    json!({"scope": "agent", "agent": "triage", "unit": "tokens", "amount": 9000.0, "period": "daily"}),
                ),
            ),
            (
                "DELETE /api/v1/spend/budgets?agent=triage&unit=tokens".to_string(),
                reply(200, json!({"deleted": true})),
            ),
            (
                "GET /api/v1/spend/alerts".to_string(),
                reply(
                    200,
                    json!({"alerts": [{"fired_at": "2026-11-03T01:00:00Z", "agent": "triage", "unit": "tokens", "threshold_pct": 80, "action": "alert", "consumed": 4000.0, "amount": 5000.0, "notified": false}]}),
                ),
            ),
            (
                "GET /api/v1/spend/usage?group=agent".to_string(),
                reply(
                    200,
                    json!({"usage": [{"group": "triage", "calls": 12, "input_tokens": 100, "output_tokens": 50, "cost_usd": 1.5, "cache_hits": 3, "cache_misses": 1, "cache_saved_usd": 0.25}]}),
                ),
            ),
        ]);
        let (base, log) = fake_control_plane(routes).await;
        (
            AgentOvenClient::new(&base).unwrap().with_kitchen("acme"),
            log,
        )
    }

    fn set(amount: f64, scope: ScopeArgs) -> SetArgs {
        SetArgs {
            amount,
            scope,
            period: None,
            alert: vec![],
            block: vec![],
            signing: SignArgs::default(),
        }
    }

    #[tokio::test]
    async fn the_budgets_are_listed_with_what_is_used_and_which_are_exhausted() {
        colored::control::set_override(false);
        let (c, _) = client().await;
        let out = run(BudgetsCommands::List, &c, false).await.unwrap();
        assert!(
            out.contains("the kitchen")
                && out.contains("$42.5000")
                && out.contains("agent triage")
                && out.contains("5000 tokens")
                && out.contains("EXHAUSTED"),
            "{out}"
        );
        let json = run(BudgetsCommands::List, &c, true).await.unwrap();
        assert!(
            serde_json::from_str::<Value>(&json).unwrap().is_array(),
            "{json}"
        );
    }

    #[tokio::test]
    async fn setting_a_budget_sends_the_scope_unit_and_thresholds_and_asks_for_no_signature_unasked(
    ) {
        colored::control::set_override(false);
        let (c, log) = client().await;
        let mut a = set(
            9000.0,
            ScopeArgs {
                agent: Some("triage".into()),
                unit: "tokens".into(),
                ..Default::default()
            },
        );
        a.period = Some("daily".into());
        a.block = vec![100];
        a.alert = vec![80, 50];
        let out = run(BudgetsCommands::Set(a), &c, false).await.unwrap();
        assert!(
            out.contains("agent triage") && out.contains("9000 tokens") && !out.contains("signed"),
            "{out}"
        );
        assert_eq!(
            log.last().body,
            json!({"agent": "triage", "unit": "tokens", "amount": 9000.0, "period": "daily",
                   "thresholds": [{"pct": 50, "action": "alert"}, {"pct": 80, "action": "alert"}, {"pct": 100, "action": "block"}]})
        );
        assert_eq!(log.len(), 1);
    }

    #[tokio::test]
    async fn a_signed_raise_goes_through_the_budget_raise_gate() {
        colored::control::set_override(false);
        let (c, log) = client().await;
        let mut a = set(
            9000.0,
            ScopeArgs {
                agent: Some("triage".into()),
                unit: "tokens".into(),
                ..Default::default()
            },
        );
        a.signing = SignArgs {
            sign: false,
            signing_token: Some("held.tok".into()),
        };
        let out = run(BudgetsCommands::Set(a), &c, false).await.unwrap();
        assert!(out.contains("(signed)"), "{out}");
        assert_eq!(log.last().headers["x-signing-token"], "held.tok");
    }

    #[tokio::test]
    async fn removing_a_budget_with_a_token_sends_it() {
        colored::control::set_override(false);
        let (c, log) = client().await;
        let a = RmArgs {
            scope: ScopeArgs {
                agent: Some("triage".into()),
                unit: "tokens".into(),
                ..Default::default()
            },
            signing: SignArgs {
                sign: false,
                signing_token: Some("t".into()),
            },
        };
        let out = run(BudgetsCommands::Rm(a), &c, false).await.unwrap();
        assert!(out.contains("removed (signed)"), "{out}");
        assert_eq!(log.last().headers["x-signing-token"], "t");
    }

    #[tokio::test]
    async fn alerts_say_whether_anyone_was_told_and_usage_shows_the_cache() {
        colored::control::set_override(false);
        let (c, _) = client().await;
        let out = run(
            BudgetsCommands::Alerts(AlertsArgs { limit: None }),
            &c,
            false,
        )
        .await
        .unwrap();
        assert!(
            out.contains("agent triage")
                && out.contains("80")
                && out.contains("4000 tokens of 5000 tokens")
                && out.contains("no (throttled"),
            "{out}"
        );
        let out = run(
            BudgetsCommands::Usage(UsageArgs {
                by: "agent".into(),
                days: None,
                limit: None,
            }),
            &c,
            false,
        )
        .await
        .unwrap();
        assert!(
            out.contains("triage")
                && out.contains("75%")
                && out.contains("$0.2500")
                && out.contains("$1.5000"),
            "{out}"
        );
    }

    /// The credential a budget counts is `--key`: `--api-key` is global, the key this command signs in with,
    /// and must never become the budget's scope.
    #[test]
    fn the_sign_in_key_is_never_the_budgets_scope() {
        use clap::Parser;
        let cli = super::super::Cli::try_parse_from([
            "agentoven",
            "--api-key",
            "secret",
            "budgets",
            "set",
            "5",
        ])
        .unwrap();
        match cli.command {
            super::super::Commands::Budgets(BudgetsCommands::Set(a)) => {
                assert!(a.scope.key.is_none() && cli.api_key.as_deref() == Some("secret"));
            }
            _ => panic!("not budgets set"),
        }
        let cli = super::super::Cli::try_parse_from([
            "agentoven",
            "budgets",
            "set",
            "5",
            "--key",
            "scopedkey:1",
        ])
        .unwrap();
        assert!(
            matches!(cli.command, super::super::Commands::Budgets(BudgetsCommands::Set(a)) if a.scope.key.as_deref() == Some("scopedkey:1"))
        );
        assert!(super::super::Cli::try_parse_from([
            "agentoven",
            "budgets",
            "set",
            "5",
            "--agent",
            "a",
            "--provider",
            "p"
        ])
        .is_err());
    }

    #[test]
    fn thresholds_are_sorted_and_refuse_zero() {
        assert_eq!(thresholds(&[], &[]).unwrap(), None);
        assert_eq!(
            thresholds(&[80, 50], &[100]).unwrap().unwrap(),
            vec![(50, "alert"), (80, "alert"), (100, "block")]
        );
        assert!(thresholds(&[0], &[]).is_err());
    }

    #[test]
    fn a_refusal_the_person_can_act_on_gets_a_hint() {
        let signature: anyhow::Error = agentoven_core::ApiError {
            status: 403,
            code: "signature_required".into(),
            message: String::new(),
            retry_after: None,
            detail: Value::Null,
        }
        .into();
        assert!(refusal_hint(&signature).unwrap().contains("--sign"));
        let limited: anyhow::Error = agentoven_core::ApiError {
            status: 429,
            code: "rate_limited".into(),
            message: String::new(),
            retry_after: Some(7),
            detail: Value::Null,
        }
        .into();
        assert!(refusal_hint(&limited).unwrap().contains("7s"));
        assert!(refusal_hint(&anyhow::anyhow!("boom")).is_none());
    }
}
