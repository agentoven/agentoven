//! `agentoven cache` — the response cache of the model router (Pro, ADR-0051).
//!
//! Off until a kitchen turns it on, for the kitchen or for one agent. A repeated request from the same
//! caller is answered without calling the provider: no cost, no budget, no rate-limit share. It is an
//! exact match, never a similar one. `--output json` prints the server's JSON as it is.

use agentoven_core::AgentOvenClient;
use clap::{Args, Subcommand};
use colored::Colorize;

use super::budgets::refusal_hint;
use super::signing::pretty;

#[derive(Subcommand)]
pub enum CacheCommands {
    /// The policies and what this server process holds for the kitchen.
    Show,
    /// Set the policy of the kitchen, or of one agent with --agent (cache:manage).
    Set(SetArgs),
    /// Remove a policy (cache:manage).
    #[command(visible_alias = "remove")]
    Rm(AgentArg),
    /// Forget everything stored for the kitchen, or for one agent, on every replica (cache:manage).
    Purge(AgentArg),
}

#[derive(Args)]
pub struct SetArgs {
    /// `off`, `deterministic` (requests that ask for temperature 0) or `always` (any eligible request).
    #[arg(value_parser = ["off", "deterministic", "always"])]
    pub mode: String,
    /// The agent the policy is for; without it, the kitchen.
    #[arg(long, default_value = "")]
    pub agent: String,
    /// How long an answer is kept, in seconds (an hour when left out; at most a week).
    #[arg(long)]
    pub ttl: Option<u64>,
    /// Let one caller be served an answer another caller caused. Off by default.
    #[arg(long)]
    pub share_across_principals: bool,
}

#[derive(Args)]
pub struct AgentArg {
    /// The agent; without it, the kitchen's own policy or the whole kitchen.
    #[arg(long, default_value = "")]
    pub agent: String,
}

pub async fn execute(
    cmd: CacheCommands,
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

fn target(agent: &str) -> String {
    if agent.is_empty() {
        "the kitchen".into()
    } else {
        format!("agent {agent}")
    }
}

pub async fn run(
    cmd: CacheCommands,
    client: &AgentOvenClient,
    json: bool,
) -> anyhow::Result<String> {
    match cmd {
        CacheCommands::Show => {
            let v = client.get_cache().await?;
            if json {
                return Ok(pretty(&v) + "\n");
            }
            let mut out = String::from("\n");
            let ps = v["policies"].as_array().cloned().unwrap_or_default();
            if ps.is_empty() {
                out.push_str("  (no policy: nothing is cached)\n");
            }
            for p in &ps {
                out.push_str(&format!(
                    "  {:<18} {:<14} ttl {}s{}\n",
                    target(p["agent"].as_str().unwrap_or("")).bold(),
                    p["mode"].as_str().unwrap_or("-"),
                    p["ttl_seconds"],
                    if p["share_across_principals"].as_bool().unwrap_or(false) {
                        ", shared across callers"
                    } else {
                        ""
                    },
                ));
            }
            let s = &v["stats"];
            let (h, m) = (
                s["hits"].as_i64().unwrap_or(0),
                s["misses"].as_i64().unwrap_or(0),
            );
            out.push_str(&format!(
                "\n  This server process: {} entries, {} bytes; {} hits, {} misses{}; {} stored.\n",
                s["entries"],
                s["bytes"],
                h,
                m,
                if h + m > 0 {
                    format!(" ({:.0}% hit rate)", h as f64 * 100.0 / (h + m) as f64)
                } else {
                    String::new()
                },
                s["stores"]
            ));
            if let Some(b) = s["bypassed"].as_object().filter(|b| !b.is_empty()) {
                let why: Vec<String> = b.iter().map(|(k, n)| format!("{k}: {n}")).collect();
                out.push_str(&format!("  Not cached: {}.\n", why.join(", ")));
            }
            Ok(out)
        }
        CacheCommands::Set(a) => {
            let p = client
                .set_cache_policy(&a.mode, &a.agent, a.ttl, a.share_across_principals)
                .await?;
            if json {
                return Ok(pretty(&p) + "\n");
            }
            Ok(format!(
                "\n  {} Cache for {}: {}. What was stored under the old policy is forgotten.\n",
                "✓".green().bold(),
                target(&a.agent).bold(),
                a.mode
            ))
        }
        CacheCommands::Rm(a) => {
            let r = client.remove_cache_policy(&a.agent).await?;
            if json {
                return Ok(pretty(&r) + "\n");
            }
            Ok(format!(
                "\n  {} Cache policy for {} removed.\n",
                "✓".green().bold(),
                target(&a.agent)
            ))
        }
        CacheCommands::Purge(a) => {
            let r = client.purge_cache(&a.agent).await?;
            if json {
                return Ok(pretty(&r) + "\n");
            }
            Ok(format!(
                "\n  {} Cache for {} purged{}.\n",
                "✓".green().bold(),
                target(&a.agent),
                r["purged_at"]
                    .as_str()
                    .map(|t| format!(" at {t}"))
                    .unwrap_or_default()
            ))
        }
    }
}

#[cfg(test)]
mod tests {
    use super::*;
    use agentoven_core::testing::{fake_control_plane, json as reply, Routes};
    use serde_json::json;

    #[tokio::test]
    async fn the_cache_is_shown_set_removed_and_purged() {
        colored::control::set_override(false);
        let (base, log) = fake_control_plane(Routes::from([
            ("GET /api/v1/cache".to_string(), reply(200, json!({
                "policies": [{"agent": "", "mode": "deterministic", "ttl_seconds": 3600, "share_across_principals": false},
                             {"agent": "bot", "mode": "always", "ttl_seconds": 120, "share_across_principals": true}],
                "stats": {"entries": 3, "bytes": 900, "hits": 3, "misses": 1, "stores": 4, "bypassed": {"secret_in_content": 2}}}))),
            ("PUT /api/v1/cache/policy".to_string(), reply(200, json!({"mode": "always"}))),
            ("DELETE /api/v1/cache/policy?agent=bot".to_string(), reply(200, json!({"deleted": true}))),
            ("POST /api/v1/cache/purge".to_string(), reply(200, json!({"purged_at": "2026-11-03T00:00:00Z"}))),
        ])).await;
        let c = AgentOvenClient::new(&base).unwrap();
        let out = run(CacheCommands::Show, &c, false).await.unwrap();
        for want in [
            "the kitchen",
            "deterministic",
            "agent bot",
            "shared across callers",
            "75% hit rate",
            "secret_in_content: 2",
        ] {
            assert!(out.contains(want), "{want} missing from {out}");
        }
        let out = run(
            CacheCommands::Set(SetArgs {
                mode: "always".into(),
                agent: "bot".into(),
                ttl: Some(120),
                share_across_principals: true,
            }),
            &c,
            false,
        )
        .await
        .unwrap();
        assert!(
            out.contains("agent bot") && out.contains("forgotten"),
            "{out}"
        );
        assert_eq!(
            log.last().body,
            json!({"agent": "bot", "mode": "always", "share_across_principals": true, "ttl_seconds": 120})
        );
        run(
            CacheCommands::Rm(AgentArg {
                agent: "bot".into(),
            }),
            &c,
            false,
        )
        .await
        .unwrap();
        let out = run(
            CacheCommands::Purge(AgentArg {
                agent: String::new(),
            }),
            &c,
            false,
        )
        .await
        .unwrap();
        assert!(
            out.contains("the kitchen") && out.contains("2026-11-03"),
            "{out}"
        );
        assert_eq!(log.last().body, json!({}));
        assert!(run(CacheCommands::Show, &c, true)
            .await
            .unwrap()
            .contains("\"policies\""));
    }
}
