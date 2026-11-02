//! `agentoven legal-hold` — place or lift a legal hold (Pro, ADR-0043).
//!
//! A hold keeps evidence from being removed. Placing one only protects, so it takes no signature. Lifting
//! one lets evidence go: it is the Legal team's decision, takes a stated reason, and is signed.
//! `--dataset <ID>` holds one dataset; without it the hold is on the kitchen's audit trail.

use agentoven_core::{AgentOvenClient, Signature};
use clap::{Args, Subcommand};
use colored::Colorize;

use super::signing::{signing_hint, SignArgs};

#[derive(Subcommand)]
pub enum LegalHoldCommands {
    /// Place a legal hold. It only protects, so it takes no signature.
    Place(PlaceArgs),
    /// Lift a legal hold: Legal only, with a reason, signed (gate legal_hold_release).
    Lift(LiftArgs),
}

#[derive(Args)]
pub struct PlaceArgs {
    /// A dataset ID; without it the kitchen's audit trail is held.
    #[arg(long)]
    pub dataset: Option<String>,
    /// Why (recorded).
    #[arg(long)]
    pub reason: String,
}

#[derive(Args)]
pub struct LiftArgs {
    /// A dataset ID; without it the hold on the kitchen's audit trail is lifted.
    #[arg(long)]
    pub dataset: Option<String>,
    /// Why (required, and signed with the decision).
    #[arg(long)]
    pub reason: String,
    #[command(flatten)]
    pub signing: SignArgs,
}

pub async fn execute(cmd: LegalHoldCommands, client: AgentOvenClient) -> anyhow::Result<()> {
    let client = &client;
    let out = match cmd {
        LegalHoldCommands::Place(a) => {
            hold(
                client,
                a.dataset.as_deref(),
                true,
                &a.reason,
                Signature::None,
            )
            .await
        }
        LegalHoldCommands::Lift(a) => {
            let signing = a.signing.resolve()?;
            hold(
                client,
                a.dataset.as_deref(),
                false,
                &a.reason,
                signing.signature(),
            )
            .await
        }
    };
    match out {
        Ok(msg) => {
            println!("{msg}");
            Ok(())
        }
        Err(e) => {
            if let Some(h) = signing_hint(&e) {
                eprintln!("{h}");
            }
            Err(e)
        }
    }
}

/// Place or lift a hold on a dataset or, with `dataset` as `None`, on the audit trail.
pub async fn hold(
    client: &AgentOvenClient,
    dataset: Option<&str>,
    held: bool,
    reason: &str,
    sig: Signature<'_>,
) -> anyhow::Result<String> {
    if !held && reason.trim().is_empty() {
        anyhow::bail!("a legal hold is lifted with a stated reason");
    }
    let (what, verb) = (
        dataset.map_or("the audit trail".to_string(), |d| format!("dataset {d}")),
        if held { "held" } else { "released" },
    );
    match dataset {
        Some(id) => client.set_dataset_legal_hold(id, held, reason, sig).await?,
        None => client.set_audit_legal_hold(held, reason, sig).await?,
    };
    let signed = if !held && !matches!(sig, Signature::None) {
        " (signed)"
    } else {
        ""
    };
    Ok(format!(
        "\n  {} Legal hold on {} {verb}{signed}.\n",
        "✓".green().bold(),
        what.bold()
    ))
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
                "PUT /api/v1/datasets/ds1/legal-hold".to_string(),
                reply(200, json!({"id": "ds1"})),
            ),
            (
                "POST /api/v1/audit/legal-hold".to_string(),
                reply(200, json!({"hold": true})),
            ),
        ]);
        let (base, log) = fake_control_plane(routes).await;
        (
            AgentOvenClient::new(&base).unwrap().with_kitchen("acme"),
            log,
        )
    }

    #[tokio::test]
    async fn placing_a_hold_asks_for_no_signature() {
        colored::control::set_override(false);
        let (c, log) = client().await;
        let msg = hold(
            &c,
            Some("ds1"),
            true,
            "litigation",
            Signature::Password("pw"),
        )
        .await
        .unwrap();
        assert!(
            msg.contains("dataset ds1") && msg.contains("held") && !msg.contains("signed"),
            "{msg}"
        );
        assert_eq!(log.calls(), vec!["PUT /api/v1/datasets/ds1/legal-hold"]);
        assert_eq!(
            log.last().body,
            json!({"held": true, "reason": "litigation"})
        );
        hold(&c, None, true, "investigation", Signature::None)
            .await
            .unwrap();
        assert_eq!(log.last().path, "/api/v1/audit/legal-hold");
    }

    #[tokio::test]
    async fn lifting_one_is_the_signed_legal_gate_on_the_dataset_or_the_audit_trail() {
        colored::control::set_override(false);
        let (c, log) = client().await;
        let msg = hold(
            &c,
            Some("ds1"),
            false,
            "case closed",
            Signature::Password("pw"),
        )
        .await
        .unwrap();
        assert!(
            msg.contains("released") && msg.contains("(signed)"),
            "{msg}"
        );
        assert_eq!(log.all()[0].body["record_id"], "dataset:ds1");
        assert_eq!(log.all()[0].body["gate"], "legal_hold_release");
        assert_eq!(log.last().headers["x-signing-token"], "tok.sig");

        hold(&c, None, false, "closed", Signature::Token("held"))
            .await
            .unwrap();
        assert_eq!(log.last().path, "/api/v1/audit/legal-hold");
        assert_eq!(log.last().body, json!({"hold": false, "reason": "closed"}));
        assert_eq!(log.last().headers["x-signing-token"], "held");

        hold(&c, None, false, "closed", Signature::Password("pw"))
            .await
            .unwrap();
        assert!(log
            .all()
            .iter()
            .any(|s| s.body["record_id"] == "audit:acme"));
    }

    #[tokio::test]
    async fn lifting_without_a_reason_is_refused_before_anything_is_sent() {
        let (c, log) = client().await;
        assert!(
            hold(&c, Some("ds1"), false, "  ", Signature::Password("pw"))
                .await
                .is_err()
        );
        assert!(log.is_empty());
    }
}
