//! `agentoven schema` — author and inspect world schemas (Pro).
//!
//! The world schema is the type system a scenario's data is validated against
//! and its assertions are typed by. In domains that already have ontologies —
//! CDISC, FHIR, SNOMED — `push` is how you bring yours in rather than authoring
//! one in a browser.

use clap::{Args, Subcommand};
use colored::Colorize;

use super::pro_gate;
use super::scenario::print_issues;

const FEATURE_NAME: &str = "Scenario Environments";
const FEATURE_KEY: &str = "scenarios";

#[derive(Subcommand)]
pub enum SchemaCommands {
    /// List world schemas.
    List,
    /// Show one schema as stored.
    Get(IdArgs),
    /// Write a schema's YAML to a local file (or stdout).
    Pull(PullArgs),
    /// Create or update a schema from a local YAML file.
    Push(PushArgs),
    /// Report what is wrong with a schema, without changing it.
    Validate(IdArgs),
    /// Report which regions of the domain the scenario suite exercises.
    Coverage(IdArgs),
    /// Show which scenarios depend on a class or Class.property.
    Usage(UsageArgs),
}

#[derive(Args)]
pub struct IdArgs {
    /// Schema id.
    pub id: String,
}

#[derive(Args)]
pub struct PullArgs {
    /// Schema id.
    pub id: String,
    /// Write here instead of stdout.
    #[arg(long, short)]
    pub out: Option<String>,
}

#[derive(Args)]
pub struct PushArgs {
    /// Path to a world schema YAML file.
    pub file: String,
    /// Create even if the id is not already present.
    #[arg(long)]
    pub create: bool,
}

#[derive(Args)]
pub struct UsageArgs {
    /// Schema id.
    pub id: String,
    /// Term to look up, e.g. Order or Order.refundedAmount.
    pub term: String,
}

pub async fn execute(cmd: SchemaCommands) -> anyhow::Result<()> {
    if !pro_gate::check_pro_feature(FEATURE_NAME, FEATURE_KEY).await? {
        return Ok(());
    }

    match cmd {
        SchemaCommands::List => list().await,
        SchemaCommands::Get(a) => get(a).await,
        SchemaCommands::Pull(a) => pull(a).await,
        SchemaCommands::Push(a) => push(a).await,
        SchemaCommands::Validate(a) => validate(a).await,
        SchemaCommands::Coverage(a) => coverage(a).await,
        SchemaCommands::Usage(a) => usage(a).await,
    }
}

async fn list() -> anyhow::Result<()> {
    println!("\n  📐 World schemas:\n");

    let client = pro_gate::build_client()?;
    let schemas: Vec<serde_json::Value> = client.raw_get("/api/v1/world-schemas").await?;

    if schemas.is_empty() {
        println!("  (none — create one with {})", "agentoven schema push <file> --create".cyan());
        return Ok(());
    }

    println!(
        "  {:<28} {:<8} {:<10} {:<10} {}",
        "ID".bold(), "VERSION".bold(), "STATUS".bold(), "CLASSES".bold(), "DOMAIN".bold()
    );
    println!("  {}", "─".repeat(84).dimmed());

    for s in &schemas {
        let classes = s["classes"].as_array().map(|c| c.len()).unwrap_or(0);
        println!(
            "  {:<28} {:<8} {:<10} {:<10} {}",
            s["id"].as_str().unwrap_or("-"),
            s["version"].as_str().unwrap_or("-"),
            s["status"].as_str().unwrap_or("-"),
            classes,
            s["domain"].as_str().unwrap_or("").dimmed()
        );
    }
    println!("\n  {} {} schema(s)", "→".dimmed(), schemas.len());
    Ok(())
}

async fn get(args: IdArgs) -> anyhow::Result<()> {
    let client = pro_gate::build_client()?;
    let yaml = client
        .raw_get_text(&format!("/api/v1/world-schemas/{}/raw", args.id))
        .await?;
    println!("\n{}", yaml);
    Ok(())
}

async fn pull(args: PullArgs) -> anyhow::Result<()> {
    let client = pro_gate::build_client()?;
    let yaml = client
        .raw_get_text(&format!("/api/v1/world-schemas/{}/raw", args.id))
        .await?;

    match args.out {
        Some(path) => {
            tokio::fs::write(&path, &yaml).await?;
            println!("\n  {} Wrote {} to {}\n", "✓".green().bold(), args.id.bold(), path.cyan());
        }
        None => println!("\n{}", yaml),
    }
    Ok(())
}

async fn push(args: PushArgs) -> anyhow::Result<()> {
    let raw = tokio::fs::read_to_string(&args.file).await?;
    let doc: serde_json::Value = serde_yaml::from_str(&raw)?;

    let id = doc["id"]
        .as_str()
        .ok_or_else(|| anyhow::anyhow!("{} has no id field", args.file))?
        .to_string();

    let client = pro_gate::build_client()?;
    let result: anyhow::Result<serde_json::Value> = if args.create {
        client.raw_post("/api/v1/world-schemas", &doc).await
    } else {
        client
            .raw_put(&format!("/api/v1/world-schemas/{}", id), &doc)
            .await
    };

    match result {
        Ok(saved) => {
            println!(
                "\n  {} {} {}",
                "✓".green().bold(),
                if args.create { "Created" } else { "Updated" },
                id.bold()
            );
            // Changing a schema version invalidates comparison against every
            // score recorded under the old one, so say so rather than letting
            // it be discovered later.
            if let Some(v) = saved["schema"]["version"].as_str() {
                println!("  {} scenarios pinning v{} are validated against this.", "→".dimmed(), v);
            }
            print_issues(saved["issues"].as_array());
        }
        Err(e) => {
            println!("\n  {} {}\n", "✗".red().bold(), e.to_string().dimmed());
            if !args.create {
                println!("  {} pass {} if this schema is new.\n", "→".dimmed(), "--create".cyan());
            }
        }
    }
    Ok(())
}

async fn validate(args: IdArgs) -> anyhow::Result<()> {
    let client = pro_gate::build_client()?;
    let schema: serde_json::Value = client
        .raw_get(&format!("/api/v1/world-schemas/{}", args.id))
        .await?;

    let out: serde_json::Value = client
        .raw_post(&format!("/api/v1/world-schemas/{}/validate", args.id), &schema)
        .await?;

    println!("\n  Validating {}\n", args.id.bold());
    print_issues(out["issues"].as_array());
    Ok(())
}

async fn coverage(args: IdArgs) -> anyhow::Result<()> {
    let client = pro_gate::build_client()?;
    let cov: serde_json::Value = client
        .raw_get(&format!("/api/v1/world-schemas/{}/coverage", args.id))
        .await?;

    let covered = cov["covered"].as_u64().unwrap_or(0);
    let total = cov["total"].as_u64().unwrap_or(0);
    let pct = cov["percent"].as_u64().unwrap_or(0);

    println!(
        "\n  📊 Coverage for {} — {}% ({}/{} enumerated states)\n",
        args.id.bold(),
        pct,
        covered,
        total
    );

    let empty = vec![];
    for row in cov["rows"].as_array().unwrap_or(&empty) {
        println!("  {}", row["path"].as_str().unwrap_or("-").cyan());
        for v in row["values"].as_array().unwrap_or(&empty) {
            let scenarios = v["scenarios"].as_array().cloned().unwrap_or_default();
            if scenarios.is_empty() {
                println!(
                    "      {} {:<20} {}",
                    "○".dimmed(),
                    v["value"].as_str().unwrap_or("-"),
                    "no scenario exercises this state".dimmed()
                );
            } else {
                let names: Vec<&str> = scenarios.iter().filter_map(|s| s.as_str()).collect();
                println!(
                    "      {} {:<20} {}",
                    "●".green(),
                    v["value"].as_str().unwrap_or("-"),
                    names.join(", ").dimmed()
                );
            }
        }
        println!();
    }
    Ok(())
}

async fn usage(args: UsageArgs) -> anyhow::Result<()> {
    let client = pro_gate::build_client()?;
    let out: serde_json::Value = client
        .raw_get(&format!(
            "/api/v1/world-schemas/{}/usage?term={}",
            args.id, args.term
        ))
        .await?;

    let scenarios = out["scenarios"].as_array().cloned().unwrap_or_default();
    if scenarios.is_empty() {
        println!(
            "\n  {} is not referenced by any scenario — safe to change.\n",
            args.term.bold()
        );
        return Ok(());
    }

    let kinds: Vec<&str> = out["kinds"].as_array().map(|k| k.iter().filter_map(|v| v.as_str()).collect()).unwrap_or_default();
    println!(
        "\n  {} {} is used by {} scenario(s) ({})\n",
        "⚠".yellow().bold(),
        args.term.bold(),
        scenarios.len(),
        kinds.join(", ").dimmed()
    );
    for s in &scenarios {
        println!("      {}", s.as_str().unwrap_or("-"));
    }
    println!(
        "\n  {} Removing or renaming it changes what those scenarios grade.\n",
        "→".dimmed()
    );
    Ok(())
}
