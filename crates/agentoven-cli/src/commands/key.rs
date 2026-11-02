//! `agentoven key` — scoped API keys (Pro).

use clap::{Args, Subcommand};
use colored::Colorize;

#[derive(Subcommand)]
pub enum KeyCommands {
    /// List the kitchen's scoped API keys.
    Ls,
    /// Give a scoped key to another person (a member of the kitchen at a rank that covers the key's role).
    Owner(OwnerArgs),
}

#[derive(Args)]
pub struct OwnerArgs {
    /// Key ID.
    pub id: String,
    /// The new owner's e-mail. Handing a key to someone else is a Head Chef's act; it is how a key whose
    /// owner left works again.
    pub email: String,
}

pub async fn execute(
    cmd: KeyCommands,
    client: agentoven_core::AgentOvenClient,
) -> anyhow::Result<()> {
    print!("{}", run(cmd, &client).await?);
    Ok(())
}

pub async fn run(
    cmd: KeyCommands,
    client: &agentoven_core::AgentOvenClient,
) -> anyhow::Result<String> {
    match cmd {
        KeyCommands::Ls => {
            let keys: Vec<serde_json::Value> = client.raw_get("/api/v1/keys").await?;
            let mut out = String::from("\n");
            if keys.is_empty() {
                out.push_str("  (no scoped keys)\n");
            } else {
                out.push_str(&format!(
                    "  {:<38} {:<22} {:<28} {}\n",
                    "ID".bold(),
                    "LABEL".bold(),
                    "OWNER".bold(),
                    "REVOKED".bold()
                ));
                for k in &keys {
                    out.push_str(&format!(
                        "  {:<38} {:<22} {:<28} {}\n",
                        k["id"].as_str().unwrap_or("-"),
                        k["label"].as_str().unwrap_or("-"),
                        k["owner_email"].as_str().unwrap_or("(none)"),
                        if k["revoked"].as_bool().unwrap_or(false) {
                            "yes"
                        } else {
                            ""
                        }
                    ));
                }
            }
            Ok(out)
        }
        KeyCommands::Owner(a) => {
            let k = client.set_scoped_key_owner(&a.id, &a.email).await?;
            Ok(format!(
                "  {} Key {} now belongs to {}.\n",
                "✓".green().bold(),
                a.id,
                k["owner_email"].as_str().unwrap_or(&a.email)
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
    async fn keys_are_listed_with_their_owner_and_given_to_someone_else() {
        colored::control::set_override(false);
        let (base, log) = fake_control_plane(Routes::from([
            ("GET /api/v1/keys".to_string(), reply(200, json!([{"id": "k9", "label": "demo", "owner_email": "ana@acme.io"}, {"id": "k8", "label": "old", "revoked": true}]))),
            ("POST /api/v1/keys/k9/owner".to_string(), reply(200, json!({"id": "k9", "owner_email": "bo@acme.io"}))),
        ])).await;
        let c = agentoven_core::AgentOvenClient::new(&base).unwrap();
        let out = run(KeyCommands::Ls, &c).await.unwrap();
        assert!(
            out.contains("ana@acme.io") && out.contains("(none)") && out.contains("yes"),
            "{out}"
        );
        let out = run(
            KeyCommands::Owner(OwnerArgs {
                id: "k9".into(),
                email: "bo@acme.io".into(),
            }),
            &c,
        )
        .await
        .unwrap();
        assert!(out.contains("bo@acme.io"), "{out}");
        assert_eq!(log.last().body, json!({"owner_email": "bo@acme.io"}));
    }
}
