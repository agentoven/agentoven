//! `agentoven limits` — the kitchen's rate limits (Pro, ADR-0051).
//!
//! A limit counts model requests, tokens or agent invocations per minute for the kitchen, one agent, one
//! API key or one provider. A caller over a limit gets 429 with `Retry-After`. `--output json` prints the
//! server's JSON as it is.

use agentoven_core::AgentOvenClient;
use clap::{Args, Subcommand};
use colored::Colorize;
use serde_json::Value;

use super::budgets::refusal_hint;
use super::signing::{clip, pretty};

#[derive(Subcommand)]
pub enum LimitsCommands {
    /// The kitchen's limits, with how many requests each has refused since the server started.
    #[command(visible_alias = "ls")]
    List,
    /// Create or replace a limit (limit:manage).
    Set(SetArgs),
    /// Remove a limit (limit:manage).
    #[command(visible_alias = "remove")]
    Rm(RmArgs),
}

#[derive(Args)]
pub struct SetArgs {
    /// What the limit counts.
    #[arg(value_parser = ["kitchen", "agent", "api_key", "provider"])]
    pub scope: String,
    /// The agent, credential subject or provider it counts; `*` gives each its own bucket. Empty for the kitchen.
    #[arg(default_value = "")]
    pub reference: String,
    /// Model requests per minute (calls to a provider).
    #[arg(long, default_value_t = 0)]
    pub rpm: u64,
    /// Tokens per minute.
    #[arg(long, default_value_t = 0)]
    pub tpm: u64,
    /// Agent invocations per minute at the API (not for a provider).
    #[arg(long, default_value_t = 0)]
    pub ipm: u64,
}

#[derive(Args)]
pub struct RmArgs {
    #[arg(value_parser = ["kitchen", "agent", "api_key", "provider"])]
    pub scope: String,
    #[arg(default_value = "")]
    pub reference: String,
}

pub async fn execute(
    cmd: LimitsCommands,
    client: AgentOvenClient,
    json: bool,
) -> anyhow::Result<()> {
    match run(cmd, &client, json).await {
        Ok(out) => {
            print!("{out}");
            Ok(())
        }
        Err(e) => {
            if let Some(h) = refusal_hint(&e) {
                eprintln!("{h}");
            }
            Err(e)
        }
    }
}

pub async fn run(
    cmd: LimitsCommands,
    client: &AgentOvenClient,
    json: bool,
) -> anyhow::Result<String> {
    match cmd {
        LimitsCommands::List => {
            let ls = client.list_limits().await?;
            if json {
                return Ok(pretty(&Value::Array(ls)) + "\n");
            }
            if ls.is_empty() {
                return Ok("\n  (no limits)\n".into());
            }
            let mut out = format!(
                "\n  {:<10} {:<28} {:>9} {:>11} {:>8} {:>9}\n",
                "SCOPE".bold(),
                "WHO".bold(),
                "REQ/MIN".bold(),
                "TOKENS/MIN".bold(),
                "INV/MIN".bold(),
                "REFUSED".bold()
            );
            for l in &ls {
                let rate = |k: &str| match l[k].as_u64().unwrap_or(0) {
                    0 => "-".to_string(),
                    n => n.to_string(),
                };
                out.push_str(&format!(
                    "  {:<10} {:<28} {:>9} {:>11} {:>8} {:>9}\n",
                    l["scope"].as_str().unwrap_or("-"),
                    clip(
                        match l["ref"].as_str().unwrap_or("") {
                            "" => "(the kitchen)",
                            r => r,
                        },
                        27
                    ),
                    rate("requests_per_minute"),
                    rate("tokens_per_minute"),
                    rate("invocations_per_minute"),
                    l["rejected"].as_u64().unwrap_or(0),
                ));
            }
            Ok(out)
        }
        LimitsCommands::Set(a) => {
            let l = client
                .set_limit(&a.scope, &a.reference, a.rpm, a.tpm, a.ipm)
                .await?;
            if json {
                return Ok(pretty(&l) + "\n");
            }
            let whose = if a.reference.is_empty() {
                "the kitchen".to_string()
            } else {
                format!("{} {}", a.scope, a.reference)
            };
            Ok(format!(
                "\n  {} Limit set for {}.\n",
                "✓".green().bold(),
                whose.bold()
            ))
        }
        LimitsCommands::Rm(a) => {
            let r = client.remove_limit(&a.scope, &a.reference).await?;
            if json {
                return Ok(pretty(&r) + "\n");
            }
            Ok(format!("\n  {} Limit removed.\n", "✓".green().bold()))
        }
    }
}

#[cfg(test)]
mod tests {
    use super::*;
    use agentoven_core::testing::{fake_control_plane, json as reply, Routes};
    use serde_json::json;

    #[tokio::test]
    async fn limits_are_listed_set_and_removed() {
        colored::control::set_override(false);
        let (base, log) = fake_control_plane(Routes::from([
            ("GET /api/v1/limits".to_string(), reply(200, json!({"limits": [
                {"scope": "agent", "ref": "triage", "requests_per_minute": 30, "tokens_per_minute": 0, "invocations_per_minute": 5, "rejected": 4},
                {"scope": "kitchen", "ref": "", "requests_per_minute": 0, "tokens_per_minute": 90000, "invocations_per_minute": 0}]}))),
            ("PUT /api/v1/limits".to_string(), reply(200, json!({"scope": "agent"}))),
            ("DELETE /api/v1/limits?scope=agent&ref=triage".to_string(), reply(200, json!({"deleted": true}))),
        ])).await;
        let c = AgentOvenClient::new(&base).unwrap();
        let out = run(LimitsCommands::List, &c, false).await.unwrap();
        assert!(
            out.contains("triage")
                && out.contains("(the kitchen)")
                && out.contains("90000")
                && out.contains("30"),
            "{out}"
        );
        let out = run(
            LimitsCommands::Set(SetArgs {
                scope: "agent".into(),
                reference: "triage".into(),
                rpm: 30,
                tpm: 0,
                ipm: 5,
            }),
            &c,
            false,
        )
        .await
        .unwrap();
        assert!(out.contains("agent triage"), "{out}");
        assert_eq!(
            log.last().body,
            json!({"scope": "agent", "ref": "triage", "requests_per_minute": 30, "tokens_per_minute": 0, "invocations_per_minute": 5})
        );
        run(
            LimitsCommands::Rm(RmArgs {
                scope: "agent".into(),
                reference: "triage".into(),
            }),
            &c,
            false,
        )
        .await
        .unwrap();
        assert!(run(
            LimitsCommands::Set(SetArgs {
                scope: "agent".into(),
                reference: "x".into(),
                rpm: 0,
                tpm: 0,
                ipm: 0
            }),
            &c,
            false
        )
        .await
        .is_err());
        assert!(run(LimitsCommands::List, &c, true)
            .await
            .unwrap()
            .trim_start()
            .starts_with('['));
    }
}
