//! `agentoven test-suite` — manage test suites (Pro).

use clap::{Args, Subcommand};
use colored::Colorize;
use std::time::Duration;

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
    /// Show a run's per-case results (and episodes, for a scenario auto-pick case).
    RunStatus(RunStatusArgs),
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
    /// Suite description.
    #[arg(long)]
    pub description: Option<String>,
    /// Test cases file (JSON array). Each case is either a plain response
    /// check ({"name","input","expected_output"}) or a scenario auto-pick
    /// check ({"name","expected_scenario_id","min_pass_rate"}) — see
    /// `agentoven scenario --help` for what a scenario ingredient resolves to.
    #[arg(long)]
    pub cases: Option<String>,
}

#[derive(Args)]
pub struct RunArgs {
    /// Test suite ID.
    pub id: String,
    /// Wait for completion and print results.
    #[arg(long)]
    pub wait: bool,
}

#[derive(Args)]
pub struct RunStatusArgs {
    /// Test suite ID.
    pub suite_id: String,
    /// Run ID.
    pub run_id: String,
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
        TestSuiteCommands::RunStatus(args) => run_status(args).await,
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
            "  {:<24} {:<24} {:<8} {:<12}",
            "NAME".bold(),
            "AGENT".bold(),
            "CASES".bold(),
            "ID".bold()
        );
        println!("  {}", "─".repeat(76).dimmed());
        for s in &suites {
            let name = s["name"].as_str().unwrap_or("-");
            // The wire field is agent_name, not agent — a suite created
            // before this fix, or via the raw API, always has it under that
            // key; there is no top-level "agent" field on the server model.
            let agent = s["agent_name"].as_str().unwrap_or("-");
            let cases = s["cases"].as_array().map(|c| c.len()).unwrap_or(0);
            let id = s["id"].as_str().unwrap_or("-");
            println!("  {:<24} {:<24} {:<8} {:<12}", name, agent, cases, id.dimmed());
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
        // The server field is agent_name — the earlier "agent" key here
        // meant every suite created through the CLI failed server-side
        // validation ("agent_name is required") before this fix.
        "agent_name": args.agent,
        "description": args.description.unwrap_or_default(),
    });

    if let Some(ref cases_file) = args.cases {
        let content = tokio::fs::read_to_string(cases_file).await?;
        let cases: serde_json::Value = serde_json::from_str(&content)?;
        body["cases"] = cases;
    } else {
        body["cases"] = serde_json::json!([]);
    }

    let client = pro_gate::build_client()?;
    match client
        .raw_post::<serde_json::Value>("/api/v1/test-suites", &body)
        .await
    {
        Ok(s) => {
            let id = s["id"].as_str().unwrap_or("-");
            println!(
                "  {} Test suite {} created (id: {}).",
                "✓".green().bold(),
                args.name.cyan(),
                id.dimmed()
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
    // The route is singular /run, not /runs — the plural form 404s.
    let r: serde_json::Value = match client
        .raw_post(&format!("/api/v1/test-suites/{}/run", args.id), &body)
        .await
    {
        Ok(r) => r,
        Err(e) => {
            println!(
                "  {} Failed: {}",
                "✗".red().bold(),
                e.to_string().dimmed()
            );
            return Ok(());
        }
    };

    let run_id = r["run_id"].as_str().unwrap_or("-").to_string();
    println!(
        "  {} Test run started (run: {})",
        "✓".green().bold(),
        run_id.cyan()
    );

    if !args.wait {
        println!(
            "  {} follow with {}\n",
            "→".dimmed(),
            format!("agentoven test-suite run-status {} {}", args.id, run_id).cyan()
        );
        return Ok(());
    }

    println!("  {} Waiting for completion...", "→".dimmed());
    let mut result = serde_json::Value::Null;
    for _ in 0..60 {
        tokio::time::sleep(Duration::from_secs(2)).await;
        result = client
            .raw_get(&format!(
                "/api/v1/test-suites/{}/runs/{}",
                args.id, run_id
            ))
            .await?;
        let status = result["status"].as_str().unwrap_or("");
        if status != "pending" && status != "running" {
            break;
        }
    }
    print_run_result(&result);
    Ok(())
}

async fn run_status(args: RunStatusArgs) -> anyhow::Result<()> {
    let client = pro_gate::build_client()?;
    let result: serde_json::Value = client
        .raw_get(&format!(
            "/api/v1/test-suites/{}/runs/{}",
            args.suite_id, args.run_id
        ))
        .await?;
    print_run_result(&result);

    // For a scenario auto-pick case, join through to the scenario run's own
    // episodes — the multi-episode breakdown a single pass/fail line can't
    // show, and the same detail `agentoven scenario run-status --episodes`
    // gives for a scenario run triggered directly.
    if let Some(results) = result["results"].as_array() {
        for r in results {
            let Some(scenario_id) = r["actual_scenario_id"].as_str() else { continue };
            let Some(trace_id) = r["trace_id"].as_str() else { continue };
            println!(
                "\n  {} episodes for {} (scenario {}):\n",
                "→".dimmed(),
                r["case_name"].as_str().unwrap_or("-").bold(),
                scenario_id.cyan()
            );
            let eps: Vec<serde_json::Value> = client
                .raw_get(&format!("/api/v1/scenario-runs/{}/episodes", trace_id))
                .await?;
            for e in &eps {
                let v = &e["verdict"];
                let status = v["status"].as_str().unwrap_or("?");
                let marker = match status {
                    "passed" => "✓".green().bold(),
                    "failed" => "✗".red().bold(),
                    _ => "∅".yellow().bold(),
                };
                println!(
                    "    {} seed {:<4} reward {:<7}",
                    marker,
                    e["seed"],
                    format!("{:.3}", v["reward"].as_f64().unwrap_or(0.0))
                );
            }
            println!("    {} {} episode(s)", "→".dimmed(), eps.len());
        }
    }
    Ok(())
}

fn print_run_result(result: &serde_json::Value) {
    let status = result["status"].as_str().unwrap_or("-");
    let marker = match status {
        "completed" => "✓".green().bold(),
        "failed" => "✗".red().bold(),
        _ => "∅".yellow().bold(),
    };
    println!("\n  {} status: {}", marker, status.bold());

    if let Some(summary) = result.get("summary") {
        println!(
            "  pass_rate {} · passed {}/{} · avg_latency {}ms · cost ${:.4}\n",
            format!(
                "{:.0}%",
                summary["pass_rate"].as_f64().unwrap_or(0.0) * 100.0
            )
            .bold(),
            summary["passed"],
            summary["total_cases"],
            summary["avg_latency_ms"],
            summary["total_cost_usd"].as_f64().unwrap_or(0.0)
        );
    }

    if let Some(results) = result["results"].as_array() {
        for r in results {
            let passed = r["passed"].as_bool().unwrap_or(false);
            let marker = if passed { "✓".green().bold() } else { "✗".red().bold() };
            let case_name = r["case_name"].as_str().unwrap_or("-");
            let extra = if let Some(scenario_id) = r["actual_scenario_id"].as_str() {
                format!(" (picked: {})", scenario_id).dimmed().to_string()
            } else if let Some(err) = r["error"].as_str() {
                if !err.is_empty() {
                    format!(" — {}", err).red().to_string()
                } else {
                    String::new()
                }
            } else {
                String::new()
            };
            println!("  {} {}{}", marker, case_name, extra);
        }
        println!();
    }
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
