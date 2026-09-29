//! `agentoven eval` — make a kitchen able to evaluate over tools and A2A (Pro).
//!
//! `install` registers the eval tools (find, run, snapshot, verify, compare) as
//! MCP tools in the kitchen and creates an evaluator agent built on them. Once
//! baked, other agents reach the evaluator over A2A like any other agent, or
//! take the eval tools directly as ingredients.

use clap::{Args, Subcommand};
use colored::Colorize;

use super::pro_gate;

const FEATURE_NAME: &str = "Evals";
const FEATURE_KEY: &str = "scenarios";

#[derive(Subcommand)]
pub enum EvalCommands {
    /// Register the eval tools and create the evaluator agent.
    Install(InstallArgs),
    /// List the eval tools an agent can use.
    Tools,
}

#[derive(Args)]
pub struct InstallArgs {
    /// Model provider for the evaluator agent. Omit to register the tools only.
    #[arg(long)]
    pub model_provider: Option<String>,
    /// Model for the evaluator agent.
    #[arg(long)]
    pub model: Option<String>,
    /// Name for the evaluator agent.
    #[arg(long, default_value = "evaluator")]
    pub agent_name: String,
    /// How the MCP gateway reaches this server; defaults to the server's own address.
    #[arg(long)]
    pub base_url: Option<String>,
    /// Bearer token the gateway sends to the eval tools (a service-account key).
    #[arg(long, env = "AGENTOVEN_EVAL_TOKEN")]
    pub token: Option<String>,
    /// Bake the evaluator after creating it.
    #[arg(long)]
    pub bake: bool,
}

pub async fn execute(cmd: EvalCommands) -> anyhow::Result<()> {
    if !pro_gate::check_pro_feature(FEATURE_NAME, FEATURE_KEY).await? {
        return Ok(());
    }
    match cmd {
        EvalCommands::Install(a) => install(a).await,
        EvalCommands::Tools => tools().await,
    }
}

async fn install(args: InstallArgs) -> anyhow::Result<()> {
    let client = pro_gate::build_client()?;
    let body = serde_json::json!({
        "base_url": args.base_url.unwrap_or_default(),
        "token": args.token.unwrap_or_default(),
        "agent_name": args.agent_name,
        "model_provider": args.model_provider.unwrap_or_default(),
        "model": args.model.unwrap_or_default(),
    });
    let out: serde_json::Value = client.raw_post("/api/v1/evals/install", &body).await?;

    println!("\n  {} Eval tools registered → {}", "✓".green().bold(), out["endpoint"].as_str().unwrap_or("-").dimmed());
    for t in out["tools"].as_array().unwrap_or(&vec![]) {
        println!("    {}", t.as_str().unwrap_or("-").cyan());
    }
    if let Some(agent) = out["agent"].as_str() {
        let created = out["agent_created"].as_bool().unwrap_or(false);
        println!(
            "\n  {} Evaluator agent {} {}",
            "✓".green().bold(),
            agent.bold(),
            if created { "created" } else { "already existed" }
        );
        if args.bake && created {
            let _: serde_json::Value = client
                .raw_post(&format!("/api/v1/agents/{}/bake", agent), &serde_json::json!({}))
                .await?;
            println!("  {} Baked — call it over A2A at /agents/{}/a2a", "✓".green().bold(), agent);
        }
    }
    for step in out["next_steps"].as_array().unwrap_or(&vec![]) {
        println!("  {} {}", "→".dimmed(), step.as_str().unwrap_or(""));
    }
    println!();
    Ok(())
}

async fn tools() -> anyhow::Result<()> {
    let client = pro_gate::build_client()?;
    let defs: Vec<serde_json::Value> = client.raw_get("/api/v1/evals/tools").await?;
    println!();
    for d in &defs {
        println!("  {}", d["name"].as_str().unwrap_or("-").cyan().bold());
        println!("    {}", d["description"].as_str().unwrap_or("").dimmed());
    }
    println!();
    Ok(())
}
