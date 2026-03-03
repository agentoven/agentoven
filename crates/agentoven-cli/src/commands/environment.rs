//! `agentoven environment` — manage deployment environments (Pro).

use clap::{Args, Subcommand};
use colored::Colorize;

use super::pro_gate;

const FEATURE_NAME: &str = "Environments";
const FEATURE_KEY: &str = "environments";

#[derive(Subcommand)]
pub enum EnvironmentCommands {
    /// List environments.
    List,
    /// Get environment details.
    Get(GetArgs),
    /// Create a new environment.
    Create(CreateArgs),
    /// Delete an environment.
    Delete(DeleteArgs),
    /// Promote an agent to a target environment.
    Promote(PromoteArgs),
}

#[derive(Args)]
pub struct GetArgs {
    /// Environment name or ID.
    pub id: String,
}

#[derive(Args)]
pub struct CreateArgs {
    /// Environment name (e.g., staging, production).
    pub name: String,
    /// Optional description.
    #[arg(long)]
    pub description: Option<String>,
}

#[derive(Args)]
pub struct DeleteArgs {
    /// Environment name or ID.
    pub id: String,
    /// Skip confirmation.
    #[arg(long)]
    pub yes: bool,
}

#[derive(Args)]
pub struct PromoteArgs {
    /// Agent name or ID to promote.
    pub agent: String,
    /// Target environment name.
    #[arg(long)]
    pub to: String,
    /// Source environment (defaults to current).
    #[arg(long)]
    pub from: Option<String>,
}

pub async fn execute(cmd: EnvironmentCommands) -> anyhow::Result<()> {
    if !pro_gate::check_pro_feature(FEATURE_NAME, FEATURE_KEY).await? {
        return Ok(());
    }

    match cmd {
        EnvironmentCommands::List => list().await,
        EnvironmentCommands::Get(args) => get(args).await,
        EnvironmentCommands::Create(args) => create(args).await,
        EnvironmentCommands::Delete(args) => delete(args).await,
        EnvironmentCommands::Promote(args) => promote(args).await,
    }
}

async fn list() -> anyhow::Result<()> {
    println!("\n  🌍 Environments:\n");

    let client = pro_gate::build_client()?;
    let resp = client
        .raw_get("/api/v1/environments")
        .await?;

    let envs: Vec<serde_json::Value> = resp;
    if envs.is_empty() {
        println!("  (no environments)");
    } else {
        println!(
            "  {:<20} {:<12} {:<32}",
            "NAME".bold(),
            "STATUS".bold(),
            "CREATED".bold()
        );
        println!("  {}", "─".repeat(68).dimmed());
        for e in &envs {
            let name = e["name"].as_str().unwrap_or("-");
            let status = e["status"].as_str().unwrap_or("active");
            let created = e["created_at"].as_str().unwrap_or("-");
            println!("  {:<20} {:<12} {:<32}", name, status, created);
        }
        println!("\n  {} {} environment(s)", "→".dimmed(), envs.len());
    }
    Ok(())
}

async fn get(args: GetArgs) -> anyhow::Result<()> {
    println!("\n  🌍 Environment: {}\n", args.id.bold());

    let client = pro_gate::build_client()?;
    match client
        .raw_get::<serde_json::Value>(&format!("/api/v1/environments/{}", args.id))
        .await
    {
        Ok(e) => {
            let json_pretty = serde_json::to_string_pretty(&e).unwrap_or_default();
            for line in json_pretty.lines() {
                println!("  {}", line);
            }
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
    println!("\n  🌍 Creating environment: {}\n", args.name.bold());

    let mut body = serde_json::json!({ "name": args.name });
    if let Some(ref desc) = args.description {
        body["description"] = serde_json::json!(desc);
    }

    let client = pro_gate::build_client()?;
    match client
        .raw_post::<serde_json::Value>("/api/v1/environments", &body)
        .await
    {
        Ok(_) => {
            println!(
                "  {} Environment {} created.",
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

async fn delete(args: DeleteArgs) -> anyhow::Result<()> {
    if !args.yes {
        let confirm = dialoguer::Confirm::new()
            .with_prompt(format!("  Delete environment {}?", args.id))
            .default(false)
            .interact()?;
        if !confirm {
            println!("  {} Cancelled.", "→".dimmed());
            return Ok(());
        }
    }

    let client = pro_gate::build_client()?;
    match client
        .raw_delete(&format!("/api/v1/environments/{}", args.id))
        .await
    {
        Ok(()) => {
            println!(
                "  {} Environment {} deleted.",
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

async fn promote(args: PromoteArgs) -> anyhow::Result<()> {
    println!(
        "\n  🚀 Promoting agent {} to {}\n",
        args.agent.bold(),
        args.to.cyan().bold()
    );

    let body = serde_json::json!({
        "agent": args.agent,
        "to_environment": args.to,
        "from_environment": args.from,
    });

    let client = pro_gate::build_client()?;
    match client
        .raw_post::<serde_json::Value>("/api/v1/promotions", &body)
        .await
    {
        Ok(_) => {
            println!(
                "  {} Agent {} promoted to {}",
                "✓".green().bold(),
                args.agent.bold(),
                args.to.cyan().bold()
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
