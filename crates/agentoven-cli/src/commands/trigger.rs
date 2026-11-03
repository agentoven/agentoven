//! `agentoven trigger` — start a recipe or an agent from an event (Pro, ADR-0047).
//!
//! A trigger runs as a service account that belongs to a person; it can start nothing the account could
//! not, and it cannot skip an approval. A webhook trigger's signing secret is shown once, when it is
//! created or rotated. `--output json` prints the server's JSON as it is.

use agentoven_core::client::triggers::NewTrigger;
use agentoven_core::AgentOvenClient;
use clap::{Args, Subcommand};
use colored::Colorize;
use serde_json::Value;

use super::budgets::refusal_hint;
use super::signing::{clip, pretty};

#[derive(Subcommand)]
pub enum TriggerCommands {
    /// The kitchen's triggers.
    #[command(visible_alias = "ls")]
    List,
    /// Create a trigger (trigger:manage).
    Create(CreateArgs),
    /// Switch a trigger on.
    Enable { id: String },
    /// Switch a trigger off.
    Disable { id: String },
    /// Delete a trigger. Its delivery log stays.
    #[command(visible_alias = "remove")]
    Rm { id: String },
    /// Replace the signing secret AgentOven generated; the new one is shown once.
    RotateSecret { id: String },
    /// Try a trigger with a sample payload. Without --run it only shows what would start.
    Fire(FireArgs),
    /// Look at an e-mail or file trigger's source now (trigger:fire).
    Poll { id: String },
    /// What happened to deliveries, newest first.
    Deliveries(DeliveriesArgs),
}

#[derive(Args)]
pub struct CreateArgs {
    pub name: String,
    /// The recipe (or, with --agent, the agent) it starts.
    pub target: String,
    /// The service account it runs as: it belongs to a person, and its role is the most the trigger can do.
    #[arg(long)]
    pub service_account: String,
    #[arg(long, default_value = "webhook", value_parser = ["webhook", "email", "file"])]
    pub kind: String,
    /// The target is an agent, run as a background task, not a recipe.
    #[arg(long)]
    pub agent: bool,
    #[arg(long)]
    pub environment: Option<String>,
    #[arg(long)]
    pub description: Option<String>,
    /// Input mapping as JSON, or @file: "$.a.b" takes a value, "text {{ $.a.b }}" puts one into text.
    #[arg(long)]
    pub mapping: Option<String>,
    /// A path the payload must have a value at (repeatable).
    #[arg(long = "require")]
    pub required: Vec<String>,
    /// The settings of the kind as JSON, or @file (a mailbox, a folder, a signature scheme).
    #[arg(long)]
    pub config: Option<String>,
    /// An existing secret to sign webhooks with, such as vault://my-secret (empty: one is generated).
    #[arg(long)]
    pub secret_ref: Option<String>,
    #[arg(long)]
    pub rate: Option<u32>,
    /// Create it switched off.
    #[arg(long)]
    pub disabled: bool,
}

#[derive(Args)]
pub struct FireArgs {
    pub id: String,
    /// The payload as JSON, or @file (empty: {}).
    #[arg(long)]
    pub payload: Option<String>,
    /// Start a real run as the trigger's account (trigger:fire), recorded as a test.
    #[arg(long)]
    pub run: bool,
}

#[derive(Args)]
pub struct DeliveriesArgs {
    #[arg(long)]
    pub trigger: Option<String>,
    #[arg(long, value_parser = ["accepted", "rejected", "duplicate", "failed"])]
    pub status: Option<String>,
    /// Only the delivery that started this run.
    #[arg(long)]
    pub run: Option<String>,
    #[arg(long)]
    pub limit: Option<u32>,
}

/// JSON given on the command line, or read from a file when it starts with `@`.
fn json_arg(what: &str, v: &str) -> anyhow::Result<Value> {
    let text = match v.strip_prefix('@') {
        Some(path) => std::fs::read_to_string(path)
            .map_err(|e| anyhow::anyhow!("{what}: cannot read {path}: {e}"))?,
        None => v.to_string(),
    };
    serde_json::from_str(&text).map_err(|e| anyhow::anyhow!("{what} is not valid JSON: {e}"))
}

pub async fn execute(
    cmd: TriggerCommands,
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
    cmd: TriggerCommands,
    client: &AgentOvenClient,
    json: bool,
) -> anyhow::Result<String> {
    match cmd {
        TriggerCommands::List => {
            let ts = client.list_triggers().await?;
            if json {
                return Ok(pretty(&Value::Array(ts)) + "\n");
            }
            if ts.is_empty() {
                return Ok("\n  (no triggers)\n".into());
            }
            let mut out = format!(
                "\n  {:<22} {:<8} {:<28} {:<5} {}\n",
                "NAME".bold(),
                "KIND".bold(),
                "STARTS".bold(),
                "STATE".bold(),
                "ID".bold()
            );
            for t in &ts {
                let target = format!(
                    "{} {}",
                    t["target_kind"].as_str().unwrap_or(""),
                    t["target"].as_str().unwrap_or("")
                );
                out.push_str(&format!(
                    "  {:<22} {:<8} {:<28} {:<5} {}\n",
                    clip(t["name"].as_str().unwrap_or("-"), 21),
                    t["kind"].as_str().unwrap_or("-"),
                    clip(&target, 27),
                    if t["enabled"].as_bool().unwrap_or(false) {
                        "on"
                    } else {
                        "off"
                    },
                    t["id"].as_str().unwrap_or("-"),
                ));
            }
            Ok(out)
        }
        TriggerCommands::Create(a) => {
            let mapping = a
                .mapping
                .as_deref()
                .map(|m| json_arg("--mapping", m))
                .transpose()?;
            let config = a
                .config
                .as_deref()
                .map(|m| json_arg("--config", m))
                .transpose()?;
            let r = client
                .create_trigger(&NewTrigger {
                    name: &a.name,
                    service_account_id: &a.service_account,
                    target: &a.target,
                    kind: Some(&a.kind),
                    target_kind: Some(if a.agent { "agent" } else { "recipe" }),
                    environment: a.environment.as_deref(),
                    description: a.description.as_deref(),
                    input_mapping: mapping,
                    required_paths: a.required.clone(),
                    config,
                    secret_ref: a.secret_ref.as_deref(),
                    rate_limit_per_min: a.rate,
                    enabled: if a.disabled { Some(false) } else { None },
                })
                .await?;
            if json {
                return Ok(pretty(&r) + "\n");
            }
            let mut out = format!(
                "\n  {} Trigger {} created.\n",
                "✓".green().bold(),
                a.name.bold()
            );
            if let Some(p) = r["trigger"]["url_path"].as_str() {
                out.push_str(&format!("  URL path: {p}\n"));
            }
            if let Some(s) = r["secret"].as_str() {
                out.push_str(&format!("  Signing secret (shown once): {}\n", s.bold()));
            }
            Ok(out)
        }
        TriggerCommands::Enable { id } => switch(client, &id, true, json).await,
        TriggerCommands::Disable { id } => switch(client, &id, false, json).await,
        TriggerCommands::Rm { id } => {
            let r = client.delete_trigger(&id).await?;
            if json {
                return Ok(pretty(&r) + "\n");
            }
            Ok(format!("\n  {} Trigger deleted.\n", "✓".green().bold()))
        }
        TriggerCommands::RotateSecret { id } => {
            let r = client.rotate_trigger_secret(&id).await?;
            if json {
                return Ok(pretty(&r) + "\n");
            }
            Ok(format!(
                "\n  {} New signing secret (shown once; the old one no longer works): {}\n",
                "✓".green().bold(),
                r["secret"].as_str().unwrap_or("").bold()
            ))
        }
        TriggerCommands::Fire(a) => {
            let payload = match a.payload.as_deref() {
                Some(p) => json_arg("--payload", p)?,
                None => serde_json::json!({}),
            };
            let r = client.fire_trigger(&a.id, &payload, !a.run).await?;
            if json {
                return Ok(pretty(&r) + "\n");
            }
            let mut out = format!(
                "\n  {} {}\n",
                "Result:".bold(),
                r["status"].as_str().unwrap_or("-")
            );
            if let Some(code) = r["code"].as_str() {
                out.push_str(&format!(
                    "  {}: {}\n",
                    code,
                    r["reason"].as_str().unwrap_or("")
                ));
            }
            if let Some(run) = r["run_id"].as_str() {
                out.push_str(&format!(
                    "  {} {}\n",
                    r["run_kind"].as_str().unwrap_or("run"),
                    run
                ));
            }
            if !r["preview"].is_null() {
                out.push_str(&format!("  Would start:\n{}\n", pretty(&r["preview"])));
            }
            Ok(out)
        }
        TriggerCommands::Poll { id } => {
            let r = client.poll_trigger(&id).await?;
            if json {
                return Ok(pretty(&r) + "\n");
            }
            Ok(format!(
                "\n  {} {} handled.\n",
                "✓".green().bold(),
                r["handled"].as_u64().unwrap_or(0)
            ))
        }
        TriggerCommands::Deliveries(a) => {
            let ds = client
                .list_trigger_deliveries(&agentoven_core::client::triggers::DeliveryFilter {
                    trigger_id: a.trigger.as_deref(),
                    status: a.status.as_deref(),
                    run_id: a.run.as_deref(),
                    limit: a.limit,
                })
                .await?;
            if json {
                return Ok(pretty(&Value::Array(ds)) + "\n");
            }
            if ds.is_empty() {
                return Ok("\n  (no deliveries)\n".into());
            }
            let mut out = format!(
                "\n  {:<26} {:<10} {:<8} {:<26} {}\n",
                "WHEN".bold(),
                "RESULT".bold(),
                "FROM".bold(),
                "WHY".bold(),
                "RUN".bold()
            );
            for d in &ds {
                let why = match d["code"].as_str() {
                    Some(c) => format!("{c} {}", d["reason"].as_str().unwrap_or("")),
                    None => String::new(),
                };
                out.push_str(&format!(
                    "  {:<26} {:<10} {:<8} {:<26} {}\n",
                    clip(d["received_at"].as_str().unwrap_or("-"), 25),
                    d["status"].as_str().unwrap_or("-"),
                    d["source"].as_str().unwrap_or("-"),
                    clip(&why, 25),
                    d["run_id"].as_str().unwrap_or("-"),
                ));
            }
            Ok(out)
        }
    }
}

async fn switch(
    client: &AgentOvenClient,
    id: &str,
    on: bool,
    json: bool,
) -> anyhow::Result<String> {
    let r = client.set_trigger_enabled(id, on).await?;
    if json {
        return Ok(pretty(&r) + "\n");
    }
    Ok(format!(
        "\n  {} Trigger switched {}.\n",
        "✓".green().bold(),
        if on { "on" } else { "off" }
    ))
}

#[cfg(test)]
mod tests {
    use super::*;
    use agentoven_core::testing::{fake_control_plane, json as reply, Routes};
    use serde_json::json;

    #[tokio::test]
    async fn triggers_are_created_listed_tried_and_their_deliveries_read() {
        colored::control::set_override(false);
        let (base, log) = fake_control_plane(Routes::from([
            ("POST /api/v1/triggers".to_string(), reply(201, json!({"trigger": {"id": "t1", "url_path": "/hooks/k/inv"}, "secret": "whsec_x"}))),
            ("GET /api/v1/triggers".to_string(), reply(200, json!({"triggers": [{"id": "t1", "name": "inv", "kind": "webhook", "target_kind": "recipe", "target": "flow", "enabled": true}]}))),
            ("POST /api/v1/triggers/t1/fire".to_string(), reply(200, json!({"status": "dry_run", "dry_run": true, "preview": {"recipe": "flow"}}))),
            ("GET /api/v1/triggers/deliveries?trigger_id=t1&status=failed".to_string(), reply(200, json!({"deliveries": [
                {"received_at": "2026-11-03T10:00:00Z", "status": "failed", "source": "webhook", "code": "permission_denied", "reason": "needs approval", "run_id": ""}]}))),
        ])).await;
        let c = AgentOvenClient::new(&base).unwrap();
        let out = run(
            TriggerCommands::Create(CreateArgs {
                name: "inv".into(),
                target: "flow".into(),
                service_account: "sa1".into(),
                kind: "webhook".into(),
                agent: false,
                environment: None,
                description: None,
                mapping: Some(r#"{"id":"$.invoice.id"}"#.into()),
                required: vec!["$.invoice.id".into()],
                config: None,
                secret_ref: None,
                rate: Some(10),
                disabled: false,
            }),
            &c,
            false,
        )
        .await
        .unwrap();
        assert!(
            out.contains("/hooks/k/inv") && out.contains("whsec_x") && out.contains("shown once"),
            "{out}"
        );
        assert_eq!(
            log.last().body["input_mapping"],
            json!({"id": "$.invoice.id"})
        );
        let out = run(TriggerCommands::List, &c, false).await.unwrap();
        assert!(
            out.contains("inv") && out.contains("recipe flow") && out.contains("on"),
            "{out}"
        );
        let out = run(
            TriggerCommands::Fire(FireArgs {
                id: "t1".into(),
                payload: Some(r#"{"a":1}"#.into()),
                run: false,
            }),
            &c,
            false,
        )
        .await
        .unwrap();
        assert!(
            out.contains("dry_run") && out.contains("Would start"),
            "{out}"
        );
        assert_eq!(
            log.last().body,
            json!({"payload": {"a": 1}, "dry_run": true})
        );
        let out = run(
            TriggerCommands::Deliveries(DeliveriesArgs {
                trigger: Some("t1".into()),
                status: Some("failed".into()),
                run: None,
                limit: None,
            }),
            &c,
            false,
        )
        .await
        .unwrap();
        assert!(
            out.contains("permission_denied") && out.contains("failed"),
            "{out}"
        );
    }

    #[tokio::test]
    async fn json_that_is_not_json_is_refused_before_anything_is_sent() {
        let (base, log) = fake_control_plane(Routes::new()).await;
        let c = AgentOvenClient::new(&base).unwrap();
        let e = run(
            TriggerCommands::Fire(FireArgs {
                id: "t1".into(),
                payload: Some("{not json".into()),
                run: true,
            }),
            &c,
            false,
        )
        .await
        .unwrap_err();
        assert!(e.to_string().contains("--payload is not valid JSON"), "{e}");
        let e = run(
            TriggerCommands::Fire(FireArgs {
                id: "t1".into(),
                payload: Some("@/no/such/file".into()),
                run: true,
            }),
            &c,
            false,
        )
        .await
        .unwrap_err();
        assert!(e.to_string().contains("cannot read"), "{e}");
        assert!(log.all().is_empty());
    }
}
