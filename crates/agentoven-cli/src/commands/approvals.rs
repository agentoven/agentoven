//! `agentoven approvals` — what is waiting for a person, and how it is being chased (Pro, ADR-0050).
//!
//! `list` shows the decisions that were announced on the kitchen's channels; `notify-status` shows one: who was
//! asked, through which channel, the reminders and escalations, and the chain of people still to ask. `claim`,
//! `snooze` and `decline` are the replies a person can give from chat and do not decide anything: a decision is
//! signed (`agentoven signature sign`, `agentoven recipe approve --sign`). `--output json` prints the server's JSON.

use agentoven_core::AgentOvenClient;
use clap::{Args, Subcommand};
use colored::Colorize;
use serde_json::Value;

use super::signing::{clip, pretty};

#[derive(Subcommand)]
pub enum ApprovalsCommands {
    /// Decisions that were announced to people: what, where it stands, how many messages went out.
    #[command(visible_alias = "ls")]
    List(ListArgs),
    /// One decision: who was told and where, reminders, escalations, claims, and who is still to be asked.
    NotifyStatus(StatusArgs),
    /// Say you are looking at a request: reminders are quiet for a while. It does not decide it.
    Claim(IdArgs),
    /// Let go of a claim.
    Unclaim(IdArgs),
    /// Say a request is not yours: it goes to the next person up at once.
    Decline(IdArgs),
    /// Quiet the chasing for a while (at most a day). The deadline does not move.
    Snooze(SnoozeArgs),
    /// How the kitchen chases: show the rules, or replace them from a JSON file (Head Chef).
    Policy(PolicyArgs),
}

#[derive(Args)]
pub struct ListArgs {
    /// open (the default), closed or all.
    #[arg(long, default_value = "open", value_parser = ["open", "closed", "all"])]
    pub status: String,
    /// Only this gate, for example tool_approval, budget_extension, recipe_gate.
    #[arg(long)]
    pub gate: Option<String>,
}

#[derive(Args)]
pub struct StatusArgs {
    /// The gate: recipe_gate, tool_approval, budget_extension, resume_guidance, environment_promotion.
    pub gate: String,
    /// The record: `<run id>:<step>` for a recipe gate, the run's trace id for a paused run, the approval id for a promotion.
    pub record_id: String,
}

#[derive(Args)]
pub struct IdArgs {
    /// The request id (from `approvals list`).
    pub id: String,
}

#[derive(Args)]
pub struct SnoozeArgs {
    /// The request id.
    pub id: String,
    /// For how long, in minutes (1 to 1440).
    #[arg(long, default_value_t = 60)]
    pub minutes: u32,
}

#[derive(Args)]
pub struct PolicyArgs {
    /// Replace the rules with the `policy` object in this JSON file (`{"policy": {...}}` or the object itself).
    #[arg(long, value_name = "FILE")]
    pub set: Option<std::path::PathBuf>,
}

pub async fn execute(
    cmd: ApprovalsCommands,
    client: AgentOvenClient,
    json: bool,
) -> anyhow::Result<()> {
    let out = match cmd {
        ApprovalsCommands::List(a) => {
            let v = client
                .list_approval_requests(Some(&a.status), a.gate.as_deref(), None)
                .await?;
            if json { pretty(&v) } else { render_list(&v, &a.status) }
        }
        ApprovalsCommands::NotifyStatus(a) => match client.approval_notify_status(&a.gate, &a.record_id).await? {
            Some(v) => if json { pretty(&v) } else { render_status(&v) },
            None => format!(
                "\n  No request was made for {} {}. It is chased only when it was announced on a channel.\n",
                a.gate.bold(),
                a.record_id.bold()
            ),
        },
        ApprovalsCommands::Claim(a) => done(client.claim_approval_request(&a.id).await?, json, "You are looking at it. Reminders are quiet for a while; it is not decided until someone signs."),
        ApprovalsCommands::Unclaim(a) => done(client.unclaim_approval_request(&a.id).await?, json, "Let go."),
        ApprovalsCommands::Decline(a) => done(client.decline_approval_request(&a.id).await?, json, "Handed on: the next person up is asked."),
        ApprovalsCommands::Snooze(a) => done(client.snooze_approval_request(&a.id, a.minutes).await?, json, &format!("Snoozed for {} minutes. The deadline does not move.", a.minutes)),
        ApprovalsCommands::Policy(a) => match a.set {
            None => {
                let v = client.get_approval_escalation().await?;
                if json { pretty(&v) } else { render_policy(&v) }
            }
            Some(path) => {
                let raw: Value = serde_json::from_str(&std::fs::read_to_string(&path)?)?;
                let policy = raw.get("policy").cloned().unwrap_or(raw);
                let v = client.set_approval_escalation(&policy).await?;
                if json { pretty(&v) } else { format!("\n  {} Rules replaced ({} set).\n", "✓".green().bold(), v["policy"].as_object().map_or(0, |m| m.len())) }
            }
        },
    };
    println!("{out}");
    Ok(())
}

fn done(v: Value, json: bool, msg: &str) -> String {
    if json {
        pretty(&v)
    } else {
        format!("\n  {} {msg}\n", "✓".green().bold())
    }
}

fn s<'a>(v: &'a Value, k: &str) -> &'a str {
    v[k].as_str().unwrap_or("")
}

fn render_list(v: &Value, status: &str) -> String {
    let reqs = v["requests"].as_array().cloned().unwrap_or_default();
    if reqs.is_empty() {
        return format!("\n  No {status} approval requests.\n");
    }
    let mut out = format!(
        "\n  {:<9} {:<22} {:<7} {:<10} {}\n",
        "ID", "GATE", "STATUS", "MESSAGES", "WHAT / WHERE IT STANDS"
    );
    for r in &reqs {
        let mut stands = Vec::new();
        if r["reminders_sent"].as_i64().unwrap_or(0) > 0 {
            stands.push(format!("{} reminder(s)", r["reminders_sent"]));
        }
        if r["stage"].as_i64().unwrap_or(0) > 0 {
            stands.push(format!(
                "escalated to stage {}",
                r["stage"].as_i64().unwrap_or(0) + 1
            ));
        }
        if !s(r, "claimed_by").is_empty() {
            stands.push(format!("{} is looking at it", s(r, "claimed_by")));
        }
        if let Some(e) = r["expires_at"].as_str() {
            stands.push(format!("refused at {e} unless decided"));
        }
        let failed = r["failed"].as_i64().unwrap_or(0);
        let msgs = if failed > 0 {
            format!("{} (+{failed} failed)", r["delivered"])
        } else {
            r["delivered"].to_string()
        };
        out.push_str(&format!(
            "  {:<9} {:<22} {:<7} {:<10} {}{}\n",
            &s(r, "id")[..s(r, "id").len().min(8)],
            s(r, "gate"),
            s(r, "status"),
            msgs,
            clip(s(r, "title"), 60),
            if stands.is_empty() {
                String::new()
            } else {
                format!("  ({})", stands.join("; ")).dimmed().to_string()
            }
        ));
    }
    out
}

fn render_status(v: &Value) -> String {
    let r = &v["request"];
    let mut out = format!(
        "\n  {} {} {}\n  {}\n",
        s(r, "gate").bold(),
        s(r, "record_id"),
        format!("[{}]", s(r, "status")).bold(),
        s(r, "title")
    );
    if !s(r, "close_reason").is_empty() {
        out.push_str(&format!("  closed: {}\n", s(r, "close_reason")));
    }
    if let Some(chain) = v["chain"].as_array() {
        out.push_str("\n  Who is asked, in order:\n");
        for st in chain {
            let people: Vec<String> = st["people"]
                .as_array()
                .cloned()
                .unwrap_or_default()
                .iter()
                .map(|p| format!("{} ({})", s(p, "name"), s(p, "role")))
                .collect();
            let mark = match s(st, "state") {
                "done" => "✓",
                "current" => "●",
                _ => "○",
            };
            out.push_str(&format!(
                "   {mark} {}  via {}\n",
                if people.is_empty() {
                    "nobody who may approve".to_string()
                } else {
                    people.join(", ")
                },
                st["channels"]
                    .as_array()
                    .map(|c| c
                        .iter()
                        .filter_map(|x| x.as_str())
                        .collect::<Vec<_>>()
                        .join(", "))
                    .unwrap_or_default()
            ));
        }
    }
    out.push_str("\n  What happened:\n");
    for e in v["events"].as_array().cloned().unwrap_or_default() {
        let mut line = format!("   {}  {}", clip(s(&e, "at"), 19), s(&e, "kind"));
        for (k, label) in [("channel", " on"), ("recipient", " →"), ("actor", " by")] {
            if !s(&e, k).is_empty() {
                line.push_str(&format!("{label} {}", s(&e, k)));
            }
        }
        if !s(&e, "detail").is_empty() {
            line.push_str(&format!(": {}", s(&e, "detail")));
        }
        out.push_str(&line);
        out.push('\n');
    }
    out
}

fn render_policy(v: &Value) -> String {
    let mut out =
        String::from("\n  Rules (empty means the default; times are in seconds, 0 means never):\n");
    let rules = v["policy"].as_object().cloned().unwrap_or_default();
    if rules.is_empty() {
        out.push_str("   none set\n");
    }
    for (k, r) in &rules {
        out.push_str(&format!("   {k}: {}\n", r));
    }
    out.push_str(&format!(
        "\n  Default: {}\n  Shortest interval allowed: {}s\n",
        v["default"], v["min_interval_seconds"]
    ));
    out
}

#[cfg(test)]
mod tests {
    use super::*;
    use agentoven_core::testing::{fake_control_plane, json as reply, Routes};
    use serde_json::json;

    async fn client() -> (AgentOvenClient, agentoven_core::testing::Log) {
        let request = json!({"id": "0f1e2d3c-aaaa-bbbb-cccc-0123456789ab", "gate": "budget_extension", "record_id": "t1", "status": "open", "title": "Decide a paused run of refunds",
            "delivered": 4, "failed": 1, "reminders_sent": 2, "stage": 1, "claimed_by": "ana", "expires_at": "2026-11-04T09:00:00Z"});
        let routes = Routes::from([
            (
                "GET /api/v1/approval-requests?status=open&gate=budget_extension".to_string(),
                reply(200, json!({"requests": [request.clone()]})),
            ),
            (
                "GET /api/v1/approval-requests?status=all&gate=budget_extension&record_id=t1"
                    .to_string(),
                reply(200, json!({"requests": [request.clone()]})),
            ),
            (
                "GET /api/v1/approval-requests/0f1e2d3c-aaaa-bbbb-cccc-0123456789ab".to_string(),
                reply(
                    200,
                    json!({
                        "request": request,
                        "events": [{"at": "2026-11-03T09:00:00Z", "kind": "notified", "channel": "ops-slack", "recipient": "Ana"}, {"at": "2026-11-03T09:05:00Z", "kind": "claimed", "actor": "slack: ana"}],
                        "chain": [{"state": "done", "people": [{"name": "Ana", "role": "chef"}], "channels": ["ops-slack"]}, {"state": "current", "people": [{"name": "Bo", "role": "head_chef"}], "channels": ["ops-slack"]}]
                    }),
                ),
            ),
            (
                "POST /api/v1/approval-requests/0f1e2d3c-aaaa-bbbb-cccc-0123456789ab/snooze"
                    .to_string(),
                reply(200, json!({"request": {}})),
            ),
            (
                "POST /api/v1/approval-requests/0f1e2d3c-aaaa-bbbb-cccc-0123456789ab/claim"
                    .to_string(),
                reply(200, json!({"request": {}})),
            ),
            (
                "PUT /api/v1/approval-escalation".to_string(),
                reply(200, json!({"policy": {"*": {}}})),
            ),
            (
                "GET /api/v1/approval-escalation".to_string(),
                reply(
                    200,
                    json!({"policy": {}, "default": {"max_reminders": 3}, "min_interval_seconds": 60}),
                ),
            ),
        ]);
        let (base, log) = fake_control_plane(routes).await;
        (
            AgentOvenClient::new(&base).unwrap().with_kitchen("acme"),
            log,
        )
    }

    #[tokio::test]
    async fn the_list_says_where_each_request_stands() {
        colored::control::set_override(false);
        let (c, log) = client().await;
        let v = c
            .list_approval_requests(Some("open"), Some("budget_extension"), None)
            .await
            .unwrap();
        let text = render_list(&v, "open");
        assert!(
            text.contains("budget_extension")
                && text.contains("4 (+1 failed)")
                && text.contains("2 reminder(s)")
                && text.contains("escalated to stage 2")
                && text.contains("ana is looking at it"),
            "{text}"
        );
        assert_eq!(
            log.last().path,
            "/api/v1/approval-requests?status=open&gate=budget_extension"
        );
    }

    #[tokio::test]
    async fn notify_status_finds_the_latest_request_and_shows_the_chain_and_timeline() {
        colored::control::set_override(false);
        let (c, log) = client().await;
        let v = c
            .approval_notify_status("budget_extension", "t1")
            .await
            .unwrap()
            .expect("a request");
        let text = render_status(&v);
        assert!(
            text.contains("Who is asked, in order")
                && text.contains("Bo (head_chef)")
                && text.contains("notified on ops-slack → Ana")
                && text.contains("claimed by slack: ana"),
            "{text}"
        );
        assert_eq!(log.calls().len(), 2);
        assert_eq!(
            log.all()[0].path,
            "/api/v1/approval-requests?status=all&gate=budget_extension&record_id=t1"
        );
        assert_eq!(
            log.all()[1].path,
            "/api/v1/approval-requests/0f1e2d3c-aaaa-bbbb-cccc-0123456789ab"
        );
    }

    #[tokio::test]
    async fn claim_and_snooze_post_to_the_request_and_say_they_decide_nothing() {
        colored::control::set_override(false);
        let (c, log) = client().await;
        let id = "0f1e2d3c-aaaa-bbbb-cccc-0123456789ab";
        c.snooze_approval_request(id, 30).await.unwrap();
        assert_eq!(log.last().body, json!({"minutes": 30}));
        c.claim_approval_request(id).await.unwrap();
        assert_eq!(
            log.last().path,
            format!("/api/v1/approval-requests/{id}/claim")
        );
        assert!(done(json!({}), false, "You are looking at it. Reminders are quiet for a while; it is not decided until someone signs.").contains("not decided until someone signs"));
    }

    #[tokio::test]
    async fn policy_is_read_and_replaced() {
        colored::control::set_override(false);
        let (c, log) = client().await;
        let text = render_policy(&c.get_approval_escalation().await.unwrap());
        assert!(text.contains("none set") && text.contains("60s"), "{text}");
        c.set_approval_escalation(&json!({"*": {"max_reminders": 2}}))
            .await
            .unwrap();
        assert_eq!(
            log.last().body,
            json!({"policy": {"*": {"max_reminders": 2}}})
        );
    }
}
