//! `agentoven dataset` — governed datasets (Pro, ADR-0039).
//!
//! A dataset is a named set of cases; a case is a snapshot with provenance and a keyed content hash; a
//! version is an append-only manifest of case hashes. Retention is `expire` (with a TTL), `persist`
//! (kept until purged, reviewed on a date) or `regulated` (kept for at least `--min-retention-days`; a
//! purge and any lowering of the minimum are signed).
//!
//! A decision that is signed takes `--sign` (the password is read from AGENTOVEN_SIGNING_PASSWORD or
//! asked for, never a flag) or `--signing-token`.

use agentoven_core::{AgentOvenClient, Signature};
use clap::{Args, Subcommand};
use colored::Colorize;
use serde_json::{json, Value};

use super::legal_hold;
use super::signing::{clip, json_arg, pretty, signing_hint, SignArgs};

#[derive(Subcommand)]
pub enum DatasetCommands {
    /// List the kitchen's datasets.
    Ls,
    /// Create a dataset.
    Create(CreateArgs),
    /// Show a dataset and its retention state.
    Get(IdArgs),
    /// Change a dataset's settings (what you do not name stays as it is).
    Update(UpdateArgs),
    /// List the cases of a dataset's draft.
    Cases(CasesArgs),
    /// Add a case to the draft.
    AddCase(AddCaseArgs),
    /// Copy a span of a trace into a case (a replay's result too: give its trace and span).
    AddFromTrace(AddFromTraceArgs),
    /// Take a case out of the draft (published versions still list it).
    RetireCase(CaseArgs),
    /// Erase a case's payload; its content hash and the version manifests stay (dataset:purge).
    EraseCase(EraseArgs),
    /// List the published versions.
    Versions(IdArgs),
    /// Show one version with its manifest, or recompute it with --verify.
    Version(VersionArgs),
    /// Publish the draft as the next version (signed if the dataset is regulated).
    Publish(PublishArgs),
    /// Export a version as JSONL (a manifest line, then the cases).
    Export(ExportArgs),
    /// Lower a regulated dataset's minimum retention (signed).
    LowerMinimum(LowerArgs),
    /// Place or lift a legal hold on a dataset (lifting is the Legal role's, signed).
    #[command(subcommand)]
    Hold(HoldCommands),
    /// Archive a dataset, then delete it, leaving a tombstone (signed if regulated).
    Purge(PurgeArgs),
    /// Datasets the retention janitor removes soon.
    Expiring(DaysArgs),
    /// Persist datasets due for review.
    Reviews(DaysArgs),
    /// What stays of purged datasets.
    Tombstones,
}

#[derive(Subcommand)]
pub enum HoldCommands {
    /// Place a hold (no signature).
    Place(HoldPlaceArgs),
    /// Lift a hold (Legal only, with a reason, signed).
    Lift(HoldLiftArgs),
}

#[derive(Args)]
pub struct IdArgs {
    /// Dataset ID.
    pub id: String,
}

#[derive(Args)]
pub struct CreateArgs {
    /// Dataset name (1 to 120 characters).
    pub name: String,
    #[arg(long)]
    pub description: Option<String>,
    /// expire, persist or regulated.
    #[arg(long = "mode")]
    pub retention_mode: Option<String>,
    /// Days until an `expire` dataset is removed.
    #[arg(long)]
    pub ttl_days: Option<u32>,
    /// When a `persist` dataset is next reviewed (RFC 3339).
    #[arg(long)]
    pub review_by: Option<String>,
    /// How long a `regulated` dataset is kept at least (default 90, never below 7).
    #[arg(long)]
    pub min_retention_days: Option<u32>,
    /// Why the data is kept (required beyond a TTL).
    #[arg(long)]
    pub purpose: Option<String>,
}

#[derive(Args)]
pub struct UpdateArgs {
    /// Dataset ID.
    pub id: String,
    #[arg(long)]
    pub description: Option<String>,
    /// expire, persist or regulated (a regulated dataset cannot leave regulated).
    #[arg(long = "mode")]
    pub retention_mode: Option<String>,
    #[arg(long)]
    pub ttl_days: Option<u32>,
    #[arg(long)]
    pub review_by: Option<String>,
    /// Raising a regulated minimum is allowed; lowering it is `dataset lower-minimum`.
    #[arg(long)]
    pub min_retention_days: Option<u32>,
    #[arg(long)]
    pub purpose: Option<String>,
}

#[derive(Args)]
pub struct CasesArgs {
    /// Dataset ID.
    pub id: String,
    /// Show the retired cases.
    #[arg(long)]
    pub retired: bool,
    #[arg(long)]
    pub limit: Option<u32>,
    #[arg(long)]
    pub offset: Option<u32>,
}

#[derive(Args)]
pub struct AddCaseArgs {
    /// Dataset ID.
    pub id: String,
    /// The case's input, as JSON.
    #[arg(long)]
    pub input: String,
    /// The expected output, as JSON.
    #[arg(long)]
    pub expected: Option<String>,
    /// Metadata, as a JSON object.
    #[arg(long)]
    pub metadata: Option<String>,
}

#[derive(Args)]
pub struct AddFromTraceArgs {
    /// Dataset ID.
    pub id: String,
    /// Trace ID.
    #[arg(long)]
    pub trace: String,
    /// Span ID.
    #[arg(long)]
    pub span: String,
    /// The expected output, as JSON, instead of the span's own output.
    #[arg(long, conflicts_with = "no_expected")]
    pub expected: Option<String>,
    /// Record no expected output (not the span's output either).
    #[arg(long)]
    pub no_expected: bool,
    /// Metadata, as a JSON object.
    #[arg(long)]
    pub metadata: Option<String>,
}

#[derive(Args)]
pub struct CaseArgs {
    /// Dataset ID.
    pub id: String,
    /// Case ID.
    pub case_id: String,
}

#[derive(Args)]
pub struct EraseArgs {
    /// Dataset ID.
    pub id: String,
    /// Case ID.
    pub case_id: String,
    /// Why (recorded).
    #[arg(long)]
    pub reason: String,
}

#[derive(Args)]
pub struct VersionArgs {
    /// Dataset ID.
    pub id: String,
    /// Version number.
    #[arg(value_name = "VERSION")]
    pub number: u64,
    /// Recompute it: the manifest digest, the chain and the content hashes.
    #[arg(long)]
    pub verify: bool,
}

#[derive(Args)]
pub struct PublishArgs {
    /// Dataset ID.
    pub id: String,
    #[command(flatten)]
    pub signing: SignArgs,
}

#[derive(Args)]
pub struct ExportArgs {
    /// Dataset ID.
    pub id: String,
    /// Version to export (the latest by default).
    #[arg(long = "dataset-version", value_name = "VERSION")]
    pub number: Option<u64>,
    /// Write the JSONL here instead of printing it.
    #[arg(long, short)]
    pub out: Option<String>,
}

#[derive(Args)]
pub struct LowerArgs {
    /// Dataset ID.
    pub id: String,
    /// The new minimum, in days (never below 7).
    #[arg(long)]
    pub days: u32,
    /// Why (required, signed with the decision).
    #[arg(long)]
    pub reason: String,
    #[command(flatten)]
    pub signing: SignArgs,
}

#[derive(Args)]
pub struct HoldPlaceArgs {
    /// Dataset ID.
    pub id: String,
    #[arg(long)]
    pub reason: String,
}

#[derive(Args)]
pub struct HoldLiftArgs {
    /// Dataset ID.
    pub id: String,
    #[arg(long)]
    pub reason: String,
    #[command(flatten)]
    pub signing: SignArgs,
}

#[derive(Args)]
pub struct PurgeArgs {
    /// Dataset ID.
    pub id: String,
    /// Why (required, signed with the decision when the dataset is regulated).
    #[arg(long)]
    pub reason: String,
    /// Do not ask for confirmation.
    #[arg(long)]
    pub yes: bool,
    #[command(flatten)]
    pub signing: SignArgs,
}

#[derive(Args)]
pub struct DaysArgs {
    /// Look this many days ahead (30 by default).
    #[arg(long)]
    pub days: Option<u32>,
}

pub async fn execute(cmd: DatasetCommands, client: AgentOvenClient) -> anyhow::Result<()> {
    if let DatasetCommands::Purge(a) = &cmd {
        if !a.yes && !confirm_purge(&a.id)? {
            println!("  Cancelled.");
            return Ok(());
        }
    }
    match run(cmd, &client).await {
        Ok(out) => {
            print!("{out}");
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

fn confirm_purge(id: &str) -> anyhow::Result<bool> {
    Ok(dialoguer::Confirm::new()
        .with_prompt(format!(
            "  Purge dataset {id}? It is archived, then deleted, and cannot be brought back"
        ))
        .default(false)
        .interact()?)
}

fn day(v: &Value) -> String {
    v.as_str()
        .map_or("-".to_string(), |s| s.chars().take(10).collect())
}

/// Run a command against `client` and return what it prints.
pub async fn run(cmd: DatasetCommands, client: &AgentOvenClient) -> anyhow::Result<String> {
    match cmd {
        DatasetCommands::Ls => {
            let ds = client.list_datasets().await?;
            let mut out = String::from("\n  🗂  Datasets\n\n");
            if ds.is_empty() {
                out.push_str("  (none — create one with `agentoven dataset create <name>`)\n");
            } else {
                out.push_str(&format!(
                    "  {:<38} {:<24} {:<11} {:>6} {:>8} {}\n",
                    "ID".bold(),
                    "NAME".bold(),
                    "RETENTION".bold(),
                    "CASES".bold(),
                    "VERSION".bold(),
                    "HOLD".bold()
                ));
                for d in &ds {
                    out.push_str(&format!(
                        "  {:<38} {:<24} {:<11} {:>6} {:>8} {}\n",
                        d["id"].as_str().unwrap_or("-"),
                        clip(d["name"].as_str().unwrap_or("-"), 23),
                        d["retention_mode"].as_str().unwrap_or("-"),
                        d["case_count"].as_u64().unwrap_or(0),
                        d["latest_version"].as_u64().unwrap_or(0),
                        if d["legal_hold"].as_bool().unwrap_or(false) {
                            "held"
                        } else {
                            ""
                        },
                    ));
                }
            }
            Ok(out)
        }
        DatasetCommands::Create(a) => {
            let mut body = json!({ "name": a.name });
            set(&mut body, "description", a.description.map(Value::from));
            set(
                &mut body,
                "retention_mode",
                a.retention_mode.map(Value::from),
            );
            set(&mut body, "ttl_days", a.ttl_days.map(Value::from));
            set(&mut body, "review_by", a.review_by.map(Value::from));
            set(
                &mut body,
                "min_retention_days",
                a.min_retention_days.map(Value::from),
            );
            set(&mut body, "purpose", a.purpose.map(Value::from));
            let d = client.create_dataset(&body).await?;
            Ok(format!(
                "  {} Created dataset {} ({})\n",
                "✓".green().bold(),
                d["name"].as_str().unwrap_or("-").bold(),
                d["id"].as_str().unwrap_or("-")
            ))
        }
        DatasetCommands::Get(a) => Ok(format!("{}\n", pretty(&client.get_dataset(&a.id).await?))),
        DatasetCommands::Update(a) => {
            // The server takes the settings whole and clears what is left out, so start from what is there.
            let cur = client.get_dataset(&a.id).await?;
            let mut body = json!({});
            for k in [
                "description",
                "retention_mode",
                "ttl_days",
                "review_by",
                "min_retention_days",
                "purpose",
            ] {
                if !cur[k].is_null() {
                    body[k] = cur[k].clone();
                }
            }
            set(&mut body, "description", a.description.map(Value::from));
            set(
                &mut body,
                "retention_mode",
                a.retention_mode.map(Value::from),
            );
            set(&mut body, "ttl_days", a.ttl_days.map(Value::from));
            set(&mut body, "review_by", a.review_by.map(Value::from));
            set(
                &mut body,
                "min_retention_days",
                a.min_retention_days.map(Value::from),
            );
            set(&mut body, "purpose", a.purpose.map(Value::from));
            client.update_dataset(&a.id, &body).await?;
            Ok(format!(
                "  {} Dataset {} updated\n",
                "✓".green().bold(),
                a.id
            ))
        }
        DatasetCommands::Cases(a) => {
            let cases = client
                .list_dataset_cases(&a.id, a.retired, a.limit, a.offset)
                .await?;
            Ok(format!("{}\n", pretty(&cases)))
        }
        DatasetCommands::AddCase(a) => {
            let mut body = json!({ "input": json_arg("--input", &a.input)? });
            if let Some(e) = &a.expected {
                body["expected_output"] = json_arg("--expected", e)?;
            }
            if let Some(m) = &a.metadata {
                body["metadata"] = json_arg("--metadata", m)?;
            }
            let c = client.add_dataset_case(&a.id, &body).await?;
            Ok(format!(
                "  {} Added case {}\n",
                "✓".green().bold(),
                c["id"].as_str().unwrap_or("-")
            ))
        }
        DatasetCommands::AddFromTrace(a) => {
            let mut body = json!({ "trace_id": a.trace, "span_id": a.span });
            if let Some(e) = &a.expected {
                body["expected_output"] = json_arg("--expected", e)?;
            } else if a.no_expected {
                body["expected_output"] = Value::Null;
            }
            if let Some(m) = &a.metadata {
                body["metadata"] = json_arg("--metadata", m)?;
            }
            let c = client.add_dataset_case_from_trace(&a.id, &body).await?;
            Ok(format!(
                "  {} Added case {} from span {}\n",
                "✓".green().bold(),
                c["id"].as_str().unwrap_or("-"),
                a.span
            ))
        }
        DatasetCommands::RetireCase(a) => {
            client.retire_dataset_case(&a.id, &a.case_id).await?;
            Ok(format!(
                "  {} Retired case {}\n",
                "✓".green().bold(),
                a.case_id
            ))
        }
        DatasetCommands::EraseCase(a) => {
            let r = client
                .erase_dataset_case(&a.id, &a.case_id, &a.reason)
                .await?;
            let again = if r["already_erased"].as_bool().unwrap_or(false) {
                " (it already was)"
            } else {
                ""
            };
            Ok(format!(
                "  {} Erased the payload of case {}{again}\n",
                "✓".green().bold(),
                a.case_id
            ))
        }
        DatasetCommands::Versions(a) => {
            let vs = client.list_dataset_versions(&a.id).await?;
            let mut out = String::from("\n");
            if vs.is_empty() {
                out.push_str("  (no published version — `agentoven dataset publish <id>`)\n");
            } else {
                out.push_str(&format!(
                    "  {:>7} {:>6} {:<12} {:<22} {}\n",
                    "VERSION".bold(),
                    "CASES".bold(),
                    "PUBLISHED".bold(),
                    "BY".bold(),
                    "SIGNED".bold()
                ));
                for v in &vs {
                    out.push_str(&format!(
                        "  {:>7} {:>6} {:<12} {:<22} {}\n",
                        v["version"].as_u64().unwrap_or(0),
                        v["case_count"].as_u64().unwrap_or(0),
                        day(&v["published_at"]),
                        clip(v["published_by"].as_str().unwrap_or("-"), 21),
                        if v["signature_id"].as_str().is_some_and(|s| !s.is_empty()) {
                            "yes"
                        } else {
                            ""
                        },
                    ));
                }
            }
            Ok(out)
        }
        DatasetCommands::Version(a) => {
            let v = if a.verify {
                client.verify_dataset_version(&a.id, a.number).await?
            } else {
                client.get_dataset_version(&a.id, a.number).await?
            };
            Ok(format!("{}\n", pretty(&v)))
        }
        DatasetCommands::Publish(a) => {
            let signing = a.signing.resolve()?;
            let r = client.publish_dataset(&a.id, signing.signature()).await?;
            let signed = if r["signed"].as_bool().unwrap_or(false) {
                " (signed)"
            } else {
                ""
            };
            Ok(format!(
                "  {} Published version {}{signed}\n",
                "✓".green().bold(),
                r["version"]["version"]
            ))
        }
        DatasetCommands::Export(a) => {
            let e = client.export_dataset(&a.id, a.number).await?;
            match a.out {
                Some(path) => {
                    std::fs::write(&path, &e.text)?;
                    Ok(format!(
                        "  {} Wrote version {} ({} case(s)) to {}\n  {} manifest digest {}\n",
                        "✓".green().bold(),
                        e.version,
                        e.cases.len(),
                        path,
                        "→".dimmed(),
                        e.manifest_digest
                    ))
                }
                None => Ok(e.text),
            }
        }
        DatasetCommands::LowerMinimum(a) => {
            let signing = a.signing.resolve()?;
            client
                .lower_dataset_minimum(&a.id, a.days, &a.reason, signing.signature())
                .await?;
            Ok(format!(
                "  {} Minimum retention of {} lowered to {} days\n",
                "✓".green().bold(),
                a.id,
                a.days
            ))
        }
        DatasetCommands::Hold(HoldCommands::Place(a)) => {
            legal_hold::hold(client, Some(&a.id), true, &a.reason, Signature::None).await
        }
        DatasetCommands::Hold(HoldCommands::Lift(a)) => {
            let signing = a.signing.resolve()?;
            legal_hold::hold(client, Some(&a.id), false, &a.reason, signing.signature()).await
        }
        DatasetCommands::Purge(a) => {
            let signing = a.signing.resolve()?;
            let r = client
                .purge_dataset(&a.id, &a.reason, signing.signature())
                .await?;
            let signed = if r["signed"].as_bool().unwrap_or(false) {
                " (signed)"
            } else {
                ""
            };
            Ok(format!(
                "  {} Purged dataset {}{signed}; a tombstone stays\n",
                "✓".green().bold(),
                a.id
            ))
        }
        DatasetCommands::Expiring(a) => {
            let ds = client.list_expiring_datasets(a.days).await?;
            Ok(format!("{}\n", pretty(&Value::Array(ds))))
        }
        DatasetCommands::Reviews(a) => {
            let ds = client.list_dataset_reviews(a.days).await?;
            Ok(format!("{}\n", pretty(&Value::Array(ds))))
        }
        DatasetCommands::Tombstones => {
            let ts = client.list_dataset_tombstones().await?;
            Ok(format!("{}\n", pretty(&Value::Array(ts))))
        }
    }
}

fn set(body: &mut Value, key: &str, v: Option<Value>) {
    if let Some(v) = v {
        body[key] = v;
    }
}

#[cfg(test)]
mod tests {
    use super::*;
    use crate::commands::{Cli, Commands};
    use agentoven_core::testing::{fake_control_plane, json as reply, Log, Reply, Routes};
    use clap::Parser;

    fn parse(args: &[&str]) -> DatasetCommands {
        let mut v = vec!["agentoven", "dataset"];
        v.extend_from_slice(args);
        match Cli::try_parse_from(v).expect("parse").command {
            Commands::Dataset(c) => c,
            _ => panic!("not a dataset command"),
        }
    }

    async fn client(routes: Routes) -> (AgentOvenClient, Log) {
        colored::control::set_override(false);
        let (base, log) = fake_control_plane(routes).await;
        (
            AgentOvenClient::new(&base).unwrap().with_kitchen("acme"),
            log,
        )
    }

    fn routes(extra: &[(&str, Reply)]) -> Routes {
        let mut r = Routes::from([
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
        ]);
        for (k, v) in extra {
            r.insert(k.to_string(), v.clone());
        }
        r
    }

    #[tokio::test]
    async fn create_sends_only_what_was_named_and_ls_shows_the_hold() {
        let (c, log) = client(routes(&[
            ("POST /api/v1/datasets", reply(201, json!({"id": "ds1", "name": "evals"}))),
            ("GET /api/v1/datasets", reply(200, json!([{"id": "ds1", "name": "evals", "retention_mode": "regulated", "case_count": 3, "latest_version": 2, "legal_hold": true}]))),
        ])).await;
        let out = run(
            parse(&[
                "create",
                "evals",
                "--mode",
                "regulated",
                "--min-retention-days",
                "365",
                "--purpose",
                "GxP",
            ]),
            &c,
        )
        .await
        .unwrap();
        assert!(out.contains("evals") && out.contains("ds1"), "{out}");
        assert_eq!(
            log.last().body,
            json!({"name": "evals", "retention_mode": "regulated", "min_retention_days": 365, "purpose": "GxP"})
        );
        let out = run(parse(&["ls"]), &c).await.unwrap();
        assert!(out.contains("regulated") && out.contains("held"), "{out}");
        assert!(run(parse(&["create", "x", "--mode", "forever"]), &c)
            .await
            .is_err());
    }

    #[tokio::test]
    async fn update_starts_from_what_is_there_so_nothing_is_cleared() {
        let (c, log) = client(routes(&[
            ("GET /api/v1/datasets/ds1", reply(200, json!({"id": "ds1", "name": "evals", "description": "keep me", "retention_mode": "expire", "ttl_days": 30, "purpose": "tests", "case_count": 3}))),
            ("PUT /api/v1/datasets/ds1", reply(200, json!({"id": "ds1"}))),
        ])).await;
        run(parse(&["update", "ds1", "--ttl-days", "60"]), &c)
            .await
            .unwrap();
        assert_eq!(
            log.last().body,
            json!({"description": "keep me", "retention_mode": "expire", "ttl_days": 60, "purpose": "tests"})
        );
    }

    #[tokio::test]
    async fn cases_go_in_as_json_and_from_a_trace_keep_no_expected_distinct() {
        let (c, log) = client(routes(&[
            (
                "POST /api/v1/datasets/ds1/cases",
                reply(201, json!({"id": "c1"})),
            ),
            (
                "POST /api/v1/datasets/ds1/cases/from-trace",
                reply(201, json!({"id": "c2"})),
            ),
            (
                "DELETE /api/v1/datasets/ds1/cases/c1",
                reply(204, json!({})),
            ),
            (
                "POST /api/v1/datasets/ds1/cases/c1/erase",
                reply(200, json!({"case": {}, "already_erased": true})),
            ),
        ]))
        .await;
        run(
            parse(&[
                "add-case",
                "ds1",
                "--input",
                r#"{"q":"refund?"}"#,
                "--expected",
                r#""yes""#,
                "--metadata",
                r#"{"tag":"x"}"#,
            ]),
            &c,
        )
        .await
        .unwrap();
        assert_eq!(
            log.last().body,
            json!({"input": {"q": "refund?"}, "expected_output": "yes", "metadata": {"tag": "x"}})
        );
        assert!(run(parse(&["add-case", "ds1", "--input", "{nope"]), &c)
            .await
            .unwrap_err()
            .to_string()
            .contains("--input"));

        run(
            parse(&["add-from-trace", "ds1", "--trace", "t1", "--span", "s1"]),
            &c,
        )
        .await
        .unwrap();
        assert_eq!(log.last().body, json!({"trace_id": "t1", "span_id": "s1"}));
        run(
            parse(&[
                "add-from-trace",
                "ds1",
                "--trace",
                "t1",
                "--span",
                "s1",
                "--no-expected",
            ]),
            &c,
        )
        .await
        .unwrap();
        assert_eq!(
            log.last().body,
            json!({"trace_id": "t1", "span_id": "s1", "expected_output": null})
        );
        run(
            parse(&[
                "add-from-trace",
                "ds1",
                "--trace",
                "t1",
                "--span",
                "s1",
                "--expected",
                r#"{"a":1}"#,
            ]),
            &c,
        )
        .await
        .unwrap();
        assert_eq!(log.last().body["expected_output"], json!({"a": 1}));

        run(parse(&["retire-case", "ds1", "c1"]), &c).await.unwrap();
        assert_eq!(log.last().method, "DELETE");
        let out = run(parse(&["erase-case", "ds1", "c1", "--reason", "gdpr"]), &c)
            .await
            .unwrap();
        assert!(out.contains("already"), "{out}");
    }

    #[tokio::test]
    async fn publish_is_signed_with_a_held_token_and_reports_it() {
        let (c, log) = client(routes(&[(
            "POST /api/v1/datasets/ds1/versions",
            reply(201, json!({"version": {"version": 3}, "signed": true})),
        )]))
        .await;
        let out = run(parse(&["publish", "ds1", "--signing-token", "held"]), &c)
            .await
            .unwrap();
        assert!(
            out.contains("version 3") && out.contains("(signed)"),
            "{out}"
        );
        assert_eq!(log.last().headers["x-signing-token"], "held");
        assert_eq!(log.len(), 1, "a token you hold signs nothing");
    }

    #[tokio::test]
    async fn the_password_comes_from_the_environment_when_sign_is_given() {
        // The only test that touches AGENTOVEN_SIGNING_PASSWORD, so it cannot race another.
        std::env::set_var(super::super::signing::PASSWORD_ENV, "env-pw");
        let (c, log) = client(routes(&[
            (
                "POST /api/v1/datasets/ds1/purge",
                reply(200, json!({"tombstone": {}, "signed": true})),
            ),
            (
                "POST /api/v1/datasets/ds1/retention/lower-minimum",
                reply(200, json!({"id": "ds1"})),
            ),
        ]))
        .await;
        let purged = run(
            parse(&["purge", "ds1", "--reason", "retention over", "--sign"]),
            &c,
        )
        .await;
        let lowered = run(
            parse(&[
                "lower-minimum",
                "ds1",
                "--days",
                "30",
                "--reason",
                "policy",
                "--sign",
            ]),
            &c,
        )
        .await;
        std::env::remove_var(super::super::signing::PASSWORD_ENV);
        assert!(purged.unwrap().contains("Purged dataset ds1"));
        lowered.unwrap();
        let seen = log.all();
        assert_eq!(
            seen[0].body["payload"],
            json!({"action": "purge", "reason": "retention over"})
        );
        assert_eq!(seen[1].body["password"], "env-pw");
        assert_eq!(seen[2].headers["x-signing-token"], "tok.sig");
        assert_eq!(
            seen[3].body["payload"],
            json!({"action": "lower_minimum", "new_minimum_days": 30, "reason": "policy"})
        );
        assert_eq!(
            seen[5].body,
            json!({"min_retention_days": 30, "reason": "policy"})
        );
    }

    #[tokio::test]
    async fn hold_place_and_lift_go_through_the_legal_gate() {
        let (c, log) = client(routes(&[(
            "PUT /api/v1/datasets/ds1/legal-hold",
            reply(200, json!({"id": "ds1"})),
        )]))
        .await;
        run(
            parse(&["hold", "place", "ds1", "--reason", "litigation"]),
            &c,
        )
        .await
        .unwrap();
        assert_eq!(
            log.last().body,
            json!({"held": true, "reason": "litigation"})
        );
        let out = run(
            parse(&[
                "hold",
                "lift",
                "ds1",
                "--reason",
                "closed",
                "--signing-token",
                "held",
            ]),
            &c,
        )
        .await
        .unwrap();
        assert!(out.contains("released"), "{out}");
        assert_eq!(log.last().headers["x-signing-token"], "held");
    }

    #[tokio::test]
    async fn export_prints_the_jsonl_or_writes_it_byte_for_byte() {
        let text = "{\"type\":\"manifest\",\"version\":2,\"manifest_digest\":\"abc\"}\n{\"type\":\"case\",\"position\":1}\n";
        let (c, _) = client(routes(&[(
            "GET /api/v1/datasets/ds1/export?version=2",
            Reply::Raw(
                200,
                text.to_string(),
                vec![("X-Manifest-Digest".into(), "abc".into())],
            ),
        )]))
        .await;
        assert_eq!(
            run(parse(&["export", "ds1", "--dataset-version", "2"]), &c)
                .await
                .unwrap(),
            text
        );
        let dir = tempfile::tempdir().unwrap();
        let path = dir.path().join("out.jsonl");
        let out = run(
            parse(&[
                "export",
                "ds1",
                "--dataset-version",
                "2",
                "--out",
                path.to_str().unwrap(),
            ]),
            &c,
        )
        .await
        .unwrap();
        assert!(out.contains("1 case") && out.contains("abc"), "{out}");
        assert_eq!(std::fs::read_to_string(path).unwrap(), text);
    }

    #[tokio::test]
    async fn versions_expiring_reviews_and_tombstones() {
        let (c, log) = client(routes(&[
            ("GET /api/v1/datasets/ds1/versions", reply(200, json!([{"version": 1, "case_count": 2, "published_at": "2026-10-30T10:00:00Z", "published_by": "ana", "signature_id": "s1"}]))),
            ("GET /api/v1/datasets/ds1/versions/1/verify", reply(200, json!({"ok": true}))),
            ("GET /api/v1/datasets/expiring?days=7", reply(200, json!([{"id": "a"}]))),
            ("GET /api/v1/datasets/reviews", reply(200, json!([]))),
            ("GET /api/v1/datasets/tombstones", reply(200, json!([{"dataset": "old"}]))),
        ])).await;
        let out = run(parse(&["versions", "ds1"]), &c).await.unwrap();
        assert!(out.contains("2026-10-30") && out.contains("yes"), "{out}");
        assert!(run(parse(&["version", "ds1", "1", "--verify"]), &c)
            .await
            .unwrap()
            .contains("true"));
        assert!(run(parse(&["expiring", "--days", "7"]), &c)
            .await
            .unwrap()
            .contains("\"a\""));
        run(parse(&["reviews"]), &c).await.unwrap();
        assert!(run(parse(&["tombstones"]), &c)
            .await
            .unwrap()
            .contains("old"));
        assert_eq!(log.len(), 5);
    }

    #[tokio::test]
    async fn a_refusal_surfaces_the_servers_code() {
        let (c, _) = client(routes(&[("PUT /api/v1/datasets/ds1/legal-hold", reply(403, json!({"error": "legal_only", "message": "only the legal team may lift a legal hold"})))])).await;
        let e = run(
            parse(&[
                "hold",
                "lift",
                "ds1",
                "--reason",
                "x",
                "--signing-token",
                "t",
            ]),
            &c,
        )
        .await
        .unwrap_err()
        .to_string();
        assert!(
            e.contains("403") && e.contains("legal_only") && e.contains("only the legal team"),
            "{e}"
        );
    }
}
