//! `agentoven kitchen` — manage kitchens (workspaces/tenants).

use clap::{Args, Subcommand};
use colored::Colorize;

#[derive(Subcommand)]
pub enum KitchenCommands {
    /// List all kitchens.
    List,
    /// Get kitchen details.
    Get(GetArgs),
    /// View current kitchen settings.
    Settings,
    /// Update kitchen settings.
    UpdateSettings(UpdateSettingsArgs),
}

#[derive(Args)]
pub struct GetArgs {
    /// Kitchen ID.
    pub id: String,
}

#[derive(Args)]
pub struct UpdateSettingsArgs {
    /// Maximum template size in bytes.
    #[arg(long)]
    pub max_template_size: Option<u64>,
    /// Trace retention TTL (e.g., "7d", "90d").
    #[arg(long)]
    pub trace_ttl: Option<String>,
    /// Audit retention TTL (e.g., "30d", "400d").
    #[arg(long)]
    pub audit_ttl: Option<String>,
    /// Max items before archival.
    #[arg(long)]
    pub max_items: Option<u64>,
    /// Enable archival (true/false).
    #[arg(long)]
    pub archive: Option<bool>,
    /// Raw JSON settings (overrides other flags).
    #[arg(long)]
    pub json: Option<String>,
}

pub async fn execute(cmd: KitchenCommands) -> anyhow::Result<()> {
    match cmd {
        KitchenCommands::List => list().await,
        KitchenCommands::Get(args) => get(args).await,
        KitchenCommands::Settings => settings().await,
        KitchenCommands::UpdateSettings(args) => update_settings(args).await,
    }
}

async fn list() -> anyhow::Result<()> {
    println!("\n  🏠 Kitchens:\n");

    let client = agentoven_core::AgentOvenClient::from_env()?;
    match client.list_kitchens().await {
        Ok(kitchens) => {
            if kitchens.is_empty() {
                println!("  (no kitchens)");
            } else {
                println!(
                    "  {:<24} {:<12} {:<16} {:<12}",
                    "NAME".bold(),
                    "PLAN".bold(),
                    "CREATED".bold(),
                    "AGENTS".bold()
                );
                println!("  {}", "─".repeat(68).dimmed());
                for k in &kitchens {
                    let name = k["name"]
                        .as_str()
                        .unwrap_or(k["id"].as_str().unwrap_or("-"));
                    let plan = k["plan"].as_str().unwrap_or("community");
                    let created = k["created_at"].as_str().unwrap_or("-");
                    let created_short = if created.len() > 10 {
                        &created[..10]
                    } else {
                        created
                    };
                    let agents = k["agent_count"].as_u64().unwrap_or(0);
                    println!(
                        "  {:<24} {:<12} {:<16} {}",
                        name, plan, created_short, agents
                    );
                }
                println!("\n  {} {} kitchen(s)", "→".dimmed(), kitchens.len());
            }
        }
        Err(e) => {
            println!(
                "  {} Could not list kitchens: {}",
                "⚠".yellow().bold(),
                e.to_string().dimmed()
            );
        }
    }
    Ok(())
}

async fn get(args: GetArgs) -> anyhow::Result<()> {
    println!("\n  🏠 Kitchen: {}\n", args.id.bold());

    let client = agentoven_core::AgentOvenClient::from_env()?;
    match client.get_kitchen(&args.id).await {
        Ok(k) => {
            println!("  {:<16} {}", "ID:".bold(), k["id"].as_str().unwrap_or("-"));
            println!(
                "  {:<16} {}",
                "Name:".bold(),
                k["name"].as_str().unwrap_or("-")
            );
            println!(
                "  {:<16} {}",
                "Plan:".bold(),
                k["plan"].as_str().unwrap_or("community")
            );
            println!(
                "  {:<16} {}",
                "Created:".bold(),
                k["created_at"].as_str().unwrap_or("-")
            );

            if let Some(limits) = k.get("plan_limits") {
                println!("\n  {}:", "Plan Limits".bold());
                if let Some(max_agents) = limits["max_agents"].as_u64() {
                    println!("    Max Agents:    {}", max_agents);
                }
                if let Some(max_recipes) = limits["max_recipes"].as_u64() {
                    println!("    Max Recipes:   {}", max_recipes);
                }
                if let Some(max_providers) = limits["max_providers"].as_u64() {
                    println!("    Max Providers: {}", max_providers);
                }
                if let Some(max_tools) = limits["max_tools"].as_u64() {
                    println!("    Max Tools:     {}", max_tools);
                }
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

async fn settings() -> anyhow::Result<()> {
    println!("\n  ⚙️  Kitchen Settings:\n");

    let client = agentoven_core::AgentOvenClient::from_env()?;
    match client.get_settings().await {
        Ok(s) => {
            let json_pretty = serde_json::to_string_pretty(&s).unwrap_or_default();
            for line in json_pretty.lines() {
                println!("  {}", line.dimmed());
            }
        }
        Err(e) => {
            println!(
                "  {} Could not fetch settings: {}",
                "⚠".yellow().bold(),
                e.to_string().dimmed()
            );
        }
    }
    Ok(())
}

async fn update_settings(args: UpdateSettingsArgs) -> anyhow::Result<()> {
    println!("\n  ⚙️  Updating settings...\n");

    let body = if let Some(ref raw) = args.json {
        serde_json::from_str(raw)?
    } else {
        let mut obj = serde_json::Map::new();
        if let Some(v) = args.max_template_size {
            obj.insert("max_template_size".into(), serde_json::json!(v));
        }
        if let Some(ref v) = args.trace_ttl {
            obj.insert("trace_ttl".into(), serde_json::json!(v));
        }
        if let Some(ref v) = args.audit_ttl {
            obj.insert("audit_ttl".into(), serde_json::json!(v));
        }
        if let Some(v) = args.max_items {
            obj.insert("max_items".into(), serde_json::json!(v));
        }
        if let Some(v) = args.archive {
            obj.insert("archive_enabled".into(), serde_json::json!(v));
        }
        serde_json::Value::Object(obj)
    };

    let client = agentoven_core::AgentOvenClient::from_env()?;
    match client.update_settings(body).await {
        Ok(_) => {
            println!("  {} Settings updated.", "✓".green().bold());
        }
        Err(e) => {
            println!("  {} Failed: {}", "✗".red().bold(), e.to_string().dimmed());
        }
    }
    Ok(())
}
