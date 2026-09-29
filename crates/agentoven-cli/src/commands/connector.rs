//! `agentoven connector` — author and inspect data connectors (Pro).
//!
//! A connector binds an ontology class to data that lives somewhere else — a
//! warehouse, a document store, an API — instead of a synthetic fixture. Like
//! scenarios and schemas, connectors are files: `pull`/`push` move them between
//! the server and a working tree so they diff and review like any other source.
//!
//! Two things make this surface different from `scenario`/`schema`. First, a
//! connector's environments are wired separately (`dev`/`prod`/...), and the
//! two readiness checks answer different questions for different people: `ready`
//! asks whether an environment is *defined*, `ready --secrets` asks whether the
//! secret it references actually resolves. Second, `test` and `describe` dial
//! the real external system, so a role that can read a connector's definition
//! is not automatically allowed to run them.

use clap::{Args, Subcommand};
use colored::Colorize;

use super::pro_gate;
use super::scenario::print_issues;

const FEATURE_NAME: &str = "Data Connectors";
const FEATURE_KEY: &str = "scenarios";

#[derive(Subcommand)]
pub enum ConnectorCommands {
    /// List connectors and their validation state.
    List,
    /// Show the drivers this server build can open.
    Kinds,
    /// Show one connector as stored.
    Get(IdArgs),
    /// Write a connector's YAML to a local file (or stdout).
    Pull(PullArgs),
    /// Create or update a connector from a local YAML file.
    Push(PushArgs),
    /// Report what is wrong with a connector, without changing it.
    Validate(IdArgs),
    /// Check whether an environment is defined for this connector.
    ///
    /// Definition-only: no secret is read and no external system is
    /// contacted. Pass --secrets to additionally confirm the referenced
    /// secret resolves — that dials the secret store, not the connector's own
    /// system, and is the check to run before promoting.
    Ready(ReadyArgs),
    /// Open a real connection for one environment and report whether it works.
    Test(EnvArgs),
    /// Open a real connection and report what the source holds.
    Describe(EnvArgs),
    /// Delete a connector.
    Delete(DeleteArgs),
}

#[derive(Args)]
pub struct IdArgs {
    /// Connector name.
    pub name: String,
}

#[derive(Args)]
pub struct PullArgs {
    /// Connector name.
    pub name: String,
    /// Write here instead of stdout.
    #[arg(long, short)]
    pub out: Option<String>,
}

#[derive(Args)]
pub struct PushArgs {
    /// Path to a connector YAML file.
    pub file: String,
    /// Create even if the name is not already present.
    #[arg(long)]
    pub create: bool,
}

#[derive(Args)]
pub struct EnvArgs {
    /// Connector name.
    pub name: String,
    /// Environment to act on (dev, staging, prod, ...).
    #[arg(long)]
    pub env: String,
}

#[derive(Args)]
pub struct ReadyArgs {
    /// Connector name.
    pub name: String,
    /// Environment to check.
    #[arg(long)]
    pub env: String,
    /// Also confirm the referenced secret resolves. Reaches the secret store,
    /// not the connector's own system — this is the promotion check.
    #[arg(long)]
    pub secrets: bool,
}

#[derive(Args)]
pub struct DeleteArgs {
    /// Connector name.
    pub name: String,
    /// Skip confirmation.
    #[arg(long)]
    pub yes: bool,
}

pub async fn execute(cmd: ConnectorCommands) -> anyhow::Result<()> {
    if !pro_gate::check_pro_feature(FEATURE_NAME, FEATURE_KEY).await? {
        return Ok(());
    }

    match cmd {
        ConnectorCommands::List => list().await,
        ConnectorCommands::Kinds => kinds().await,
        ConnectorCommands::Get(a) => get(a).await,
        ConnectorCommands::Pull(a) => pull(a).await,
        ConnectorCommands::Push(a) => push(a).await,
        ConnectorCommands::Validate(a) => validate(a).await,
        ConnectorCommands::Ready(a) => ready(a).await,
        ConnectorCommands::Test(a) => test(a).await,
        ConnectorCommands::Describe(a) => describe(a).await,
        ConnectorCommands::Delete(a) => delete(a).await,
    }
}

async fn list() -> anyhow::Result<()> {
    println!("\n  🔌 Data connectors:\n");

    let client = pro_gate::build_client()?;
    let connectors: Vec<serde_json::Value> = client.raw_get("/api/v1/connectors").await?;

    if connectors.is_empty() {
        println!("  (none — create one with {})", "agentoven connector push <file> --create".cyan());
        return Ok(());
    }

    println!(
        "  {:<24} {:<12} {:<20} {:<10}",
        "NAME".bold(), "KIND".bold(), "ENVIRONMENTS".bold(), "STATE".bold()
    );
    println!("  {}", "─".repeat(70).dimmed());

    for c in &connectors {
        let envs: Vec<&str> = c["environments"]
            .as_object()
            .map(|m| m.keys().map(|s| s.as_str()).collect())
            .unwrap_or_default();
        let issues = c["issues"].as_array().cloned().unwrap_or_default();
        let errors = issues.iter().filter(|i| i["level"].as_str() == Some("error")).count();

        let state = if errors > 0 {
            format!("{} error(s)", errors).red().to_string()
        } else {
            "ok".green().to_string()
        };
        println!(
            "  {:<24} {:<12} {:<20} {}",
            c["name"].as_str().unwrap_or("-"),
            c["kind"].as_str().unwrap_or("-"),
            envs.join(", "),
            state
        );
    }
    println!("\n  {} {} connector(s)", "→".dimmed(), connectors.len());
    Ok(())
}

async fn kinds() -> anyhow::Result<()> {
    let client = pro_gate::build_client()?;
    let out: serde_json::Value = client.raw_get("/api/v1/connectors/kinds").await?;

    println!("\n  Supported connector kinds:\n");
    for k in out["kinds"].as_array().unwrap_or(&vec![]) {
        println!("    {}", k.as_str().unwrap_or("-").cyan());
    }
    println!();
    Ok(())
}

async fn get(args: IdArgs) -> anyhow::Result<()> {
    let client = pro_gate::build_client()?;
    let yaml = client
        .raw_get_text(&format!("/api/v1/connectors/{}/raw", args.name))
        .await?;
    println!("\n{}", yaml);
    Ok(())
}

async fn pull(args: PullArgs) -> anyhow::Result<()> {
    let client = pro_gate::build_client()?;
    let yaml = client
        .raw_get_text(&format!("/api/v1/connectors/{}/raw", args.name))
        .await?;

    match args.out {
        Some(path) => {
            tokio::fs::write(&path, &yaml).await?;
            println!("\n  {} Wrote {} to {}\n", "✓".green().bold(), args.name.bold(), path.cyan());
        }
        None => println!("\n{}", yaml),
    }
    Ok(())
}

async fn push(args: PushArgs) -> anyhow::Result<()> {
    let raw = tokio::fs::read_to_string(&args.file).await?;
    let doc: serde_json::Value = serde_yaml::from_str(&raw)?;

    let name = doc["name"]
        .as_str()
        .ok_or_else(|| anyhow::anyhow!("{} has no name field", args.file))?
        .to_string();

    let client = pro_gate::build_client()?;
    let result: anyhow::Result<serde_json::Value> = if args.create {
        client.raw_post("/api/v1/connectors", &doc).await
    } else {
        client
            .raw_put(&format!("/api/v1/connectors/{}", name), &doc)
            .await
    };

    match result {
        Ok(saved) => {
            println!(
                "\n  {} {} {}",
                "✓".green().bold(),
                if args.create { "Created" } else { "Updated" },
                name.bold()
            );
            print_issues(saved["issues"].as_array());
        }
        Err(e) => {
            println!("\n  {} {}\n", "✗".red().bold(), e.to_string().dimmed());
            if !args.create {
                println!("  {} pass {} if this connector is new.\n", "→".dimmed(), "--create".cyan());
            }
        }
    }
    Ok(())
}

async fn validate(args: IdArgs) -> anyhow::Result<()> {
    let client = pro_gate::build_client()?;
    let rec: serde_json::Value = client
        .raw_get(&format!("/api/v1/connectors/{}", args.name))
        .await?;

    let out: serde_json::Value = client
        .raw_post("/api/v1/connectors/validate", &rec)
        .await?;

    println!("\n  Validating {}\n", args.name.bold());
    print_issues(out["issues"].as_array());
    Ok(())
}

async fn ready(args: ReadyArgs) -> anyhow::Result<()> {
    let client = pro_gate::build_client()?;
    let path = format!(
        "/api/v1/connectors/{}/ready?env={}{}",
        args.name,
        args.env,
        if args.secrets { "&secrets=1" } else { "" }
    );
    let out: serde_json::Value = client.raw_get(&path).await?;

    let what = if args.secrets { "provisioning" } else { "the definition" };
    println!(
        "\n  Checking {} for {} in {}\n",
        what,
        args.name.bold(),
        args.env.cyan()
    );
    print_issues(out["issues"].as_array());
    Ok(())
}

async fn test(args: EnvArgs) -> anyhow::Result<()> {
    let client = pro_gate::build_client()?;
    let out: serde_json::Value = client
        .raw_post(
            &format!("/api/v1/connectors/{}/test?env={}", args.name, args.env),
            &serde_json::json!({}),
        )
        .await?;

    let elapsed = out["elapsed_ms"].as_i64().unwrap_or(0);
    if out["ok"].as_bool().unwrap_or(false) {
        println!(
            "\n  {} {} ({}) reachable in {} — {}ms\n",
            "✓".green().bold(),
            args.name.bold(),
            args.env.cyan(),
            out["kind"].as_str().unwrap_or("-"),
            elapsed
        );
    } else {
        println!(
            "\n  {} {} not reachable in {} ({}ms)\n      {}\n",
            "✗".red().bold(),
            args.name.bold(),
            args.env.cyan(),
            elapsed,
            out["error"].as_str().unwrap_or("unknown error").dimmed()
        );
    }
    Ok(())
}

async fn describe(args: EnvArgs) -> anyhow::Result<()> {
    let client = pro_gate::build_client()?;
    let path = format!("/api/v1/connectors/{}/describe?env={}", args.name, args.env);

    match client.raw_get::<serde_json::Value>(&path).await {
        Ok(out) => {
            let empty = vec![];
            let types = out["types"].as_array().unwrap_or(&empty);
            println!(
                "\n  🔎 {} ({}) — {} type(s) discovered\n",
                args.name.bold(),
                args.env.cyan(),
                types.len()
            );
            for t in types {
                println!("  {}", t["locator"].as_str().unwrap_or("-").cyan().bold());
                if let Some(suggested) = t["suggested_class"].as_str() {
                    println!("      suggested class: {}", suggested);
                }
                if let Some(props) = t["properties"].as_object() {
                    for (k, v) in props {
                        println!("      {:<24} {}", k.dimmed(), v.as_str().unwrap_or(""));
                    }
                }
                println!();
            }
        }
        Err(e) => println!("\n  {} {}\n", "✗".red().bold(), e.to_string().dimmed()),
    }
    Ok(())
}

async fn delete(args: DeleteArgs) -> anyhow::Result<()> {
    if !args.yes {
        println!(
            "\n  {} This deletes the connector definition (never the external data it points at).\n  Re-run with {} to confirm.\n",
            "⚠".yellow().bold(),
            "--yes".cyan()
        );
        return Ok(());
    }

    let client = pro_gate::build_client()?;
    client
        .raw_delete(&format!("/api/v1/connectors/{}", args.name))
        .await?;
    println!("\n  {} Deleted {}\n", "✓".green().bold(), args.name.bold());
    Ok(())
}
