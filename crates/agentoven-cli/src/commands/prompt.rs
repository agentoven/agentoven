//! `agentoven prompt` — manage versioned prompt templates.

use clap::{Args, Subcommand};
use colored::Colorize;

#[derive(Subcommand)]
pub enum PromptCommands {
    /// List all prompt templates.
    List,
    /// Add a new prompt template.
    Add(AddArgs),
    /// Get a specific prompt.
    Get(GetArgs),
    /// Update a prompt template.
    Update(UpdateArgs),
    /// Remove a prompt template.
    Remove(RemoveArgs),
    /// Validate a prompt template.
    Validate(ValidateArgs),
    /// List version history for a prompt.
    Versions(VersionsArgs),
}

#[derive(Args)]
pub struct AddArgs {
    /// Prompt name.
    pub name: String,
    /// Template text (inline).
    #[arg(long, short)]
    pub template: Option<String>,
    /// Template from file.
    #[arg(long)]
    pub from_file: Option<String>,
    /// Template variables (comma-separated).
    #[arg(long)]
    pub variables: Option<String>,
}

#[derive(Args)]
pub struct GetArgs {
    /// Prompt name.
    pub name: String,
}

#[derive(Args)]
pub struct UpdateArgs {
    /// Prompt name.
    pub name: String,
    /// New template text.
    #[arg(long, short)]
    pub template: Option<String>,
    /// New template from file.
    #[arg(long)]
    pub from_file: Option<String>,
}

#[derive(Args)]
pub struct RemoveArgs {
    /// Prompt name.
    pub name: String,
    /// Skip confirmation.
    #[arg(long)]
    pub force: bool,
}

#[derive(Args)]
pub struct ValidateArgs {
    /// Prompt name.
    pub name: String,
}

#[derive(Args)]
pub struct VersionsArgs {
    /// Prompt name.
    pub name: String,
}

pub async fn execute(cmd: PromptCommands) -> anyhow::Result<()> {
    match cmd {
        PromptCommands::List => list().await,
        PromptCommands::Add(args) => add(args).await,
        PromptCommands::Get(args) => get(args).await,
        PromptCommands::Update(args) => update(args).await,
        PromptCommands::Remove(args) => remove(args).await,
        PromptCommands::Validate(args) => validate(args).await,
        PromptCommands::Versions(args) => versions(args).await,
    }
}

async fn list() -> anyhow::Result<()> {
    println!("\n  {} Prompt Templates:\n", "📝".to_string());

    let client = agentoven_core::AgentOvenClient::from_env()?;
    match client.list_prompts().await {
        Ok(prompts) => {
            if prompts.is_empty() {
                println!("  (no prompts — use `agentoven prompt add`)");
            } else {
                println!(
                    "  {:<24} {:<8} {:<20} {:<8}",
                    "NAME".bold(), "VERSION".bold(), "UPDATED".bold(), "VARS".bold()
                );
                println!("  {}", "─".repeat(64).dimmed());
                for p in &prompts {
                    let name = p["name"].as_str().unwrap_or("-");
                    let ver = p["version"].as_u64().unwrap_or(0);
                    let updated = p["updated_at"].as_str().unwrap_or("-");
                    let updated_short = if updated.len() > 16 { &updated[..16] } else { updated };
                    let vars = p["variables"].as_array().map(|a| a.len()).unwrap_or(0);
                    println!("  {:<24} v{:<7} {:<20} {}", name, ver, updated_short, vars);
                }
                println!("\n  {} {} prompt(s)", "→".dimmed(), prompts.len());
            }
        }
        Err(e) => {
            println!("  {} Could not reach control plane: {}", "⚠".yellow().bold(), e.to_string().dimmed());
        }
    }
    Ok(())
}

async fn add(args: AddArgs) -> anyhow::Result<()> {
    println!("\n  {} Adding prompt: {}\n", "📝".to_string(), args.name.bold());

    let template = if let Some(ref t) = args.template {
        t.clone()
    } else if let Some(ref path) = args.from_file {
        tokio::fs::read_to_string(path).await?
    } else {
        anyhow::bail!("Provide --template or --from-file");
    };

    let mut body = serde_json::json!({
        "name": args.name,
        "template": template,
    });
    if let Some(vars) = &args.variables {
        let var_list: Vec<&str> = vars.split(',').map(|s| s.trim()).collect();
        body["variables"] = serde_json::json!(var_list);
    }

    let client = agentoven_core::AgentOvenClient::from_env()?;
    match client.add_prompt(body).await {
        Ok(result) => {
            let ver = result["version"].as_u64().unwrap_or(1);
            println!("  {} Prompt '{}' v{} created!", "✓".green().bold(), args.name, ver);
        }
        Err(e) => {
            println!("  {} Failed: {}", "✗".red().bold(), e.to_string().dimmed());
        }
    }
    Ok(())
}

async fn get(args: GetArgs) -> anyhow::Result<()> {
    println!("\n  {} Prompt: {}\n", "📝".to_string(), args.name.bold());

    let client = agentoven_core::AgentOvenClient::from_env()?;
    match client.get_prompt(&args.name).await {
        Ok(p) => {
            println!("  {:<16} {}", "Name:".bold(), p["name"].as_str().unwrap_or("-"));
            println!("  {:<16} v{}", "Version:".bold(), p["version"].as_u64().unwrap_or(0));
            println!("  {:<16} {}", "Updated:".bold(), p["updated_at"].as_str().unwrap_or("-"));
            if let Some(vars) = p["variables"].as_array() {
                let var_names: Vec<&str> = vars.iter().filter_map(|v| v.as_str()).collect();
                println!("  {:<16} {}", "Variables:".bold(), var_names.join(", "));
            }
            println!("\n  {}:", "Template".bold());
            if let Some(tmpl) = p["template"].as_str() {
                for line in tmpl.lines() {
                    println!("    {}", line.dimmed());
                }
            }
        }
        Err(e) => {
            println!("  {} Not found: {}", "⚠".yellow().bold(), e.to_string().dimmed());
        }
    }
    Ok(())
}

async fn update(args: UpdateArgs) -> anyhow::Result<()> {
    println!("\n  {} Updating prompt: {}\n", "📝".to_string(), args.name.bold());

    let template = if let Some(ref t) = args.template {
        Some(t.clone())
    } else if let Some(ref path) = args.from_file {
        Some(tokio::fs::read_to_string(path).await?)
    } else {
        None
    };

    let mut body = serde_json::json!({});
    if let Some(t) = template {
        body["template"] = serde_json::json!(t);
    }

    let client = agentoven_core::AgentOvenClient::from_env()?;
    match client.update_prompt(&args.name, body).await {
        Ok(result) => {
            let ver = result["version"].as_u64().unwrap_or(0);
            println!("  {} Prompt '{}' updated to v{}.", "✓".green().bold(), args.name, ver);
        }
        Err(e) => {
            println!("  {} Failed: {}", "✗".red().bold(), e.to_string().dimmed());
        }
    }
    Ok(())
}

async fn remove(args: RemoveArgs) -> anyhow::Result<()> {
    if !args.force {
        let confirm = dialoguer::Confirm::new()
            .with_prompt(format!("  Remove prompt '{}'?", args.name))
            .default(false)
            .interact()?;
        if !confirm {
            println!("  {} Cancelled.", "→".dimmed());
            return Ok(());
        }
    }

    let client = agentoven_core::AgentOvenClient::from_env()?;
    match client.delete_prompt(&args.name).await {
        Ok(()) => println!("  {} Prompt '{}' removed.", "✓".green().bold(), args.name),
        Err(e) => println!("  {} Failed: {}", "✗".red().bold(), e.to_string().dimmed()),
    }
    Ok(())
}

async fn validate(args: ValidateArgs) -> anyhow::Result<()> {
    println!("\n  {} Validating prompt: {}...\n", "✅".to_string(), args.name.bold());

    let client = agentoven_core::AgentOvenClient::from_env()?;
    match client.validate_prompt(&args.name).await {
        Ok(result) => {
            let valid = result["valid"].as_bool().unwrap_or(false);
            if valid {
                println!("  {} Prompt is valid!", "✓".green().bold());
            } else {
                println!("  {} Validation issues found:", "⚠".yellow().bold());
                if let Some(issues) = result["issues"].as_array() {
                    for issue in issues {
                        let sev = issue["severity"].as_str().unwrap_or("info");
                        let msg = issue["message"].as_str().unwrap_or("-");
                        let icon = match sev {
                            "error" => "✗".red().to_string(),
                            "warning" => "⚠".yellow().to_string(),
                            _ => "ℹ".blue().to_string(),
                        };
                        println!("    {} {}", icon, msg);
                    }
                }
            }
        }
        Err(e) => {
            println!("  {} Failed: {}", "✗".red().bold(), e.to_string().dimmed());
        }
    }
    Ok(())
}

async fn versions(args: VersionsArgs) -> anyhow::Result<()> {
    println!("\n  {} Versions for prompt: {}\n", "📝".to_string(), args.name.bold());

    let client = agentoven_core::AgentOvenClient::from_env()?;
    match client.prompt_versions(&args.name).await {
        Ok(versions) => {
            if versions.is_empty() {
                println!("  (no version history)");
            } else {
                println!(
                    "  {:<8} {:<24} {:<8}",
                    "VERSION".bold(), "UPDATED".bold(), "VARS".bold()
                );
                println!("  {}", "─".repeat(42).dimmed());
                for v in &versions {
                    let ver = v["version"].as_u64().unwrap_or(0);
                    let updated = v["updated_at"].as_str().unwrap_or("-");
                    let vars = v["variables"].as_array().map(|a| a.len()).unwrap_or(0);
                    println!("  v{:<7} {:<24} {}", ver, updated, vars);
                }
            }
        }
        Err(e) => {
            println!("  {} Failed: {}", "✗".red().bold(), e.to_string().dimmed());
        }
    }
    Ok(())
}
