//! `agentoven tool` — manage MCP tools.

use clap::{Args, Subcommand};
use colored::Colorize;

#[derive(Subcommand)]
pub enum ToolCommands {
    /// List all registered MCP tools.
    List,
    /// Add a new MCP tool.
    Add(AddArgs),
    /// Get details of a specific tool.
    Get(GetArgs),
    /// Update a tool's configuration.
    Update(UpdateArgs),
    /// Remove a tool.
    Remove(RemoveArgs),
}

#[derive(Args)]
pub struct AddArgs {
    /// Tool name.
    pub name: String,
    /// Tool description.
    #[arg(long, short)]
    pub description: Option<String>,
    /// Input schema as JSON string.
    #[arg(long)]
    pub schema: Option<String>,
    /// Input schema from file.
    #[arg(long)]
    pub schema_file: Option<String>,
}

#[derive(Args)]
pub struct GetArgs {
    /// Tool name.
    pub name: String,
}

#[derive(Args)]
pub struct UpdateArgs {
    /// Tool name.
    pub name: String,
    /// New description.
    #[arg(long, short)]
    pub description: Option<String>,
    /// New schema as JSON.
    #[arg(long)]
    pub schema: Option<String>,
}

#[derive(Args)]
pub struct RemoveArgs {
    /// Tool name.
    pub name: String,
    /// Skip confirmation.
    #[arg(long)]
    pub force: bool,
}

pub async fn execute(cmd: ToolCommands) -> anyhow::Result<()> {
    match cmd {
        ToolCommands::List => list().await,
        ToolCommands::Add(args) => add(args).await,
        ToolCommands::Get(args) => get(args).await,
        ToolCommands::Update(args) => update(args).await,
        ToolCommands::Remove(args) => remove(args).await,
    }
}

async fn list() -> anyhow::Result<()> {
    println!("\n  {} MCP Tools:\n", "🔧".to_string());

    let client = agentoven_core::AgentOvenClient::from_env()?;
    match client.list_tools().await {
        Ok(tools) => {
            if tools.is_empty() {
                println!("  (no tools registered — use `agentoven tool add`)");
            } else {
                println!(
                    "  {:<24} {:<40} {:<20}",
                    "NAME".bold(), "DESCRIPTION".bold(), "UPDATED".bold()
                );
                println!("  {}", "─".repeat(84).dimmed());
                for t in &tools {
                    let name = t["name"].as_str().unwrap_or("-");
                    let desc = t["description"].as_str().unwrap_or("-");
                    let desc_trunc = if desc.len() > 38 { &desc[..38] } else { desc };
                    let updated = t["updated_at"].as_str().unwrap_or("-");
                    let updated_short = if updated.len() > 16 { &updated[..16] } else { updated };
                    println!("  {:<24} {:<40} {:<20}", name, desc_trunc, updated_short);
                }
                println!("\n  {} {} tool(s)", "→".dimmed(), tools.len());
            }
        }
        Err(e) => {
            println!("  {} Could not reach control plane: {}", "⚠".yellow().bold(), e.to_string().dimmed());
        }
    }
    Ok(())
}

async fn add(args: AddArgs) -> anyhow::Result<()> {
    println!("\n  {} Adding tool: {}\n", "🔧".to_string(), args.name.bold());

    let schema = if let Some(ref s) = args.schema {
        Some(serde_json::from_str::<serde_json::Value>(s)?)
    } else if let Some(ref path) = args.schema_file {
        let content = tokio::fs::read_to_string(path).await?;
        Some(serde_json::from_str::<serde_json::Value>(&content)?)
    } else {
        None
    };

    let mut body = serde_json::json!({ "name": args.name });
    if let Some(desc) = &args.description {
        body["description"] = serde_json::json!(desc);
    }
    if let Some(s) = schema {
        body["input_schema"] = s;
    }

    let client = agentoven_core::AgentOvenClient::from_env()?;
    match client.add_tool(body).await {
        Ok(_) => {
            println!("  {} Tool '{}' added!", "✓".green().bold(), args.name);
        }
        Err(e) => {
            println!("  {} Failed: {}", "✗".red().bold(), e.to_string().dimmed());
        }
    }
    Ok(())
}

async fn get(args: GetArgs) -> anyhow::Result<()> {
    println!("\n  {} Tool: {}\n", "🔧".to_string(), args.name.bold());

    let client = agentoven_core::AgentOvenClient::from_env()?;
    match client.get_tool(&args.name).await {
        Ok(t) => {
            println!("  {:<16} {}", "Name:".bold(), t["name"].as_str().unwrap_or("-"));
            println!("  {:<16} {}", "Description:".bold(), t["description"].as_str().unwrap_or("-"));
            println!("  {:<16} {}", "Updated:".bold(), t["updated_at"].as_str().unwrap_or("-"));
            if let Some(schema) = t.get("input_schema") {
                println!("\n  {}:", "Input Schema".bold());
                println!("  {}", serde_json::to_string_pretty(schema)?.dimmed());
            }
        }
        Err(e) => {
            println!("  {} Not found: {}", "⚠".yellow().bold(), e.to_string().dimmed());
        }
    }
    Ok(())
}

async fn update(args: UpdateArgs) -> anyhow::Result<()> {
    println!("\n  {} Updating tool: {}\n", "🔧".to_string(), args.name.bold());

    let mut body = serde_json::json!({});
    if let Some(desc) = &args.description {
        body["description"] = serde_json::json!(desc);
    }
    if let Some(s) = &args.schema {
        body["input_schema"] = serde_json::from_str(s)?;
    }

    let client = agentoven_core::AgentOvenClient::from_env()?;
    match client.update_tool(&args.name, body).await {
        Ok(_) => println!("  {} Tool '{}' updated.", "✓".green().bold(), args.name),
        Err(e) => println!("  {} Failed: {}", "✗".red().bold(), e.to_string().dimmed()),
    }
    Ok(())
}

async fn remove(args: RemoveArgs) -> anyhow::Result<()> {
    if !args.force {
        let confirm = dialoguer::Confirm::new()
            .with_prompt(format!("  Remove tool '{}'?", args.name))
            .default(false)
            .interact()?;
        if !confirm {
            println!("  {} Cancelled.", "→".dimmed());
            return Ok(());
        }
    }

    let client = agentoven_core::AgentOvenClient::from_env()?;
    match client.delete_tool(&args.name).await {
        Ok(()) => println!("  {} Tool '{}' removed.", "✓".green().bold(), args.name),
        Err(e) => println!("  {} Failed: {}", "✗".red().bold(), e.to_string().dimmed()),
    }
    Ok(())
}
