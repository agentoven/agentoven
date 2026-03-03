//! `agentoven test-suite` — manage test suites (Pro).

use clap::{Args, Subcommand};
use colored::Colorize;

use super::pro_gate;

const FEATURE_NAME: &str = "Test Suites";
const FEATURE_KEY: &str = "test_suites";

#[derive(Subcommand)]
pub enum TestSuiteCommands {
    /// List test suites.
    List,
    /// Get test suite details.
    Get(GetArgs),
    /// Create a test suite.
    Create(CreateArgs),
    /// Run a test suite.
    Run(RunArgs),
    /// Delete a test suite.
    Delete(DeleteArgs),
}

#[derive(Args)]
pub struct GetArgs {
    /// Test suite ID.
    pub id: String,
}

#[derive(Args)]
pub struct CreateArgs {
    /// Suite name.
    pub name: String,
    /// Agent to test.
    #[arg(long)]
    pub agent: String,
    /// Test cases file (JSON).
    #[arg(long)]
    pub cases: Option<String>,
}

#[derive(Args)]
pub struct RunArgs {
    /// Test suite ID.
    pub id: String,
    /// Wait for completion.
    #[arg(long)]
    pub wait: bool,
}

#[derive(Args)]
pub struct DeleteArgs {
    /// Test suite ID.
    pub id: String,
    /// Skip confirmation.
    #[arg(long)]
    pub yes: bool,
}

pub async fn execute(cmd: TestSuiteCommands) -> anyhow::Result<()> {
    if !pro_gate::check_pro_feature(FEATURE_NAME, FEATURE_KEY).await? {
        return Ok(());
    }

    match cmd {
        TestSuiteCommands::List => list().await,
        TestSuiteCommands::Get(args) => get(args).await,
        TestSuiteCommands::Create(args) => create(args).await,
        TestSuiteCommands::Run(args) => run(args).await,
        TestSuiteCommands::Delete(args) => delete(args).await,
    }
}

async fn list() -> anyhow::Result<()> {
    println!("\n  🧪 Test Suites:\n");

    let client = pro_gate::build_client()?;
    let suites: Vec<serde_json::Value> = client.raw_get("/api/v1/test-suites").await?;

    if suites.is_empty() {
        println!("  (no test suites)");
    } else {
        println!(
            "  {:<24} {:<16} {:<12} {:<20}",
            "NAME".bold(),
            "AGENT".bold(),
            "CASES".bold(),
            "LAST RUN".bold()
        );
        println!("  {}", "─".repeat(76).dimmed());
        for s in &suites {
            let name = s["name"].as_str().unwrap_or("-");
            let agent = s["agent"].as_str().unwrap_or("-");
            let cases = s["case_count"].as_u64().unwrap_or(0);
            let last_run = s["last_run_at"].as_str().unwrap_or("never");
            println!("  {:<24} {:<16} {:<12} {:<20}", name, agent, cases, last_run);
        }
        println!("\n  {} {} suite(s)", "→".dimmed(), suites.len());
    }
    Ok(())
}

async fn get(args: GetArgs) -> anyhow::Result<()> {
    let client = pro_gate::build_client()?;
    match client
        .raw_get::<serde_json::Value>(&format!("/api/v1/test-suites/{}", args.id))
        .await
    {
        Ok(s) => {
            let json_pretty = serde_json::to_string_pretty(&s).unwrap_or_default();
            println!();
            for line in json_pretty.lines() {
                println!("  {}", line);
            }
            println!();
        }
        Err(e) => {
            println!(
                "  {} Not found: {}",
                "⚠".yellow().bold(),
                e.to_string().dimmed()
            );
        }
    }
    Ok(())
}

async fn create(args: CreateArgs) -> anyhow::Result<()> {
    println!("\n  🧪 Creating test suite: {}\n", args.name.bold());

    let mut body = serde_json::json!({
        "name": args.name,
        "agent": args.agent,
    });

    if let Some(ref cases_file) = args.cases {
        let content = tokio::fs::read_to_string(cases_file).await?;
        let cases: serde_json::Value = serde_json::from_str(&content)?;
        body["cases"] = cases;
    }

    let client = pro_gate::build_client()?;
    match client
        .raw_post::<serde_json::Value>("/api/v1/test-suites", &body)
        .await
    {
        Ok(_) => {
            println!(
                "  {} Test suite {} created.",
                "✓".green().bold(),
                args.name.cyan()
            );
        }
        Err(e) => {
            println!(
                "  {} Failed: {}",
                "✗".red().bold(),
                e.to_string().dimmed()
            );
        }
    }
    Ok(())
}

async fn run(args: RunArgs) -> anyhow::Result<()> {
    println!("\n  🧪 Running test suite: {}\n", args.id.bold());

    let body = serde_json::json!({});
    let client = pro_gate::build_client()?;
    match client
        .raw_post::<serde_json::Value>(
            &format!("/api/v1/test-suites/{}/runs", args.id),
            &body,
        )
        .await
    {
        Ok(r) => {
            let run_id = r["id"].as_str().unwrap_or("-");
            println!(
                "  {} Test run started (run: {})",
                "✓".green().bold(),
                run_id.cyan()
            );

            if args.wait {
                println!("  {} Waiting for completion...", "→".dimmed());
                // Future: poll for completion
            }
        }
        Err(e) => {
            println!(
                "  {} Failed: {}",
                "✗".red().bold(),
                e.to_string().dimmed()
            );
        }
    }
    Ok(())
}

async fn delete(args: DeleteArgs) -> anyhow::Result<()> {
    if !args.yes {
        let confirm = dialoguer::Confirm::new()
            .with_prompt(format!("  Delete test suite {}?", args.id))
            .default(false)
            .interact()?;
        if !confirm {
            println!("  {} Cancelled.", "→".dimmed());
            return Ok(());
        }
    }

    let client = pro_gate::build_client()?;
    match client
        .raw_delete(&format!("/api/v1/test-suites/{}", args.id))
        .await
    {
        Ok(()) => {
            println!(
                "  {} Test suite {} deleted.",
                "✓".green().bold(),
                args.id.dimmed()
            );
        }
        Err(e) => {
            println!(
                "  {} Failed: {}",
                "✗".red().bold(),
                e.to_string().dimmed()
            );
        }
    }
    Ok(())
}
