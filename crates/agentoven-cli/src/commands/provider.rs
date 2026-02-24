//! `agentoven provider` — manage model providers.

use clap::{Args, Subcommand};
use colored::Colorize;

#[derive(Subcommand)]
pub enum ProviderCommands {
    /// List all registered model providers.
    List,
    /// Add a new model provider.
    Add(AddArgs),
    /// Get details of a specific provider.
    Get(GetArgs),
    /// Update a provider's configuration.
    Update(UpdateArgs),
    /// Remove a model provider.
    Remove(RemoveArgs),
    /// Test provider connectivity and credentials.
    Test(TestArgs),
    /// Discover models available from a provider.
    Discover(DiscoverArgs),
}

#[derive(Args)]
pub struct AddArgs {
    /// Provider name (e.g., "my-openai").
    pub name: String,
    /// Provider kind: openai, azure-openai, anthropic, ollama, litellm.
    #[arg(long, short)]
    pub kind: String,
    /// API key (or set via env).
    #[arg(long)]
    pub api_key: Option<String>,
    /// Base URL / endpoint.
    #[arg(long)]
    pub base_url: Option<String>,
    /// Default model name.
    #[arg(long)]
    pub model: Option<String>,
}

#[derive(Args)]
pub struct GetArgs {
    /// Provider name.
    pub name: String,
}

#[derive(Args)]
pub struct UpdateArgs {
    /// Provider name.
    pub name: String,
    /// New API key.
    #[arg(long)]
    pub api_key: Option<String>,
    /// New base URL.
    #[arg(long)]
    pub base_url: Option<String>,
    /// New default model.
    #[arg(long)]
    pub model: Option<String>,
    /// Enable/disable the provider.
    #[arg(long)]
    pub enabled: Option<bool>,
}

#[derive(Args)]
pub struct RemoveArgs {
    /// Provider name.
    pub name: String,
    /// Skip confirmation.
    #[arg(long)]
    pub force: bool,
}

#[derive(Args)]
pub struct TestArgs {
    /// Provider name.
    pub name: String,
}

#[derive(Args)]
pub struct DiscoverArgs {
    /// Provider name.
    pub name: String,
}

pub async fn execute(cmd: ProviderCommands) -> anyhow::Result<()> {
    match cmd {
        ProviderCommands::List => list().await,
        ProviderCommands::Add(args) => add(args).await,
        ProviderCommands::Get(args) => get(args).await,
        ProviderCommands::Update(args) => update(args).await,
        ProviderCommands::Remove(args) => remove(args).await,
        ProviderCommands::Test(args) => test(args).await,
        ProviderCommands::Discover(args) => discover(args).await,
    }
}

async fn list() -> anyhow::Result<()> {
    println!("\n  ⚙️ Model Providers:\n");

    let client = agentoven_core::AgentOvenClient::from_env()?;
    match client.list_providers().await {
        Ok(providers) => {
            if providers.is_empty() {
                println!("  (no providers registered — use `agentoven provider add`)");
            } else {
                println!(
                    "  {:<20} {:<16} {:<12} {:<8}",
                    "NAME".bold(),
                    "KIND".bold(),
                    "DEFAULT MODEL".bold(),
                    "STATUS".bold()
                );
                println!("  {}", "─".repeat(60).dimmed());
                for p in &providers {
                    let name = p["name"].as_str().unwrap_or("-");
                    let kind = p["kind"].as_str().unwrap_or("-");
                    let model = p["default_model"].as_str().unwrap_or("-");
                    let enabled = p["enabled"].as_bool().unwrap_or(true);
                    let status = if enabled { "🟢 on" } else { "⚪ off" };
                    println!("  {:<20} {:<16} {:<12} {:<8}", name, kind, model, status);
                }
                println!("\n  {} {} provider(s)", "→".dimmed(), providers.len());
            }
        }
        Err(e) => {
            println!(
                "  {} Could not reach control plane: {}",
                "⚠".yellow().bold(),
                e.to_string().dimmed()
            );
        }
    }
    Ok(())
}

async fn add(args: AddArgs) -> anyhow::Result<()> {
    println!("\n  ⚙️ Adding provider: {}\n", args.name.bold());

    let client = agentoven_core::AgentOvenClient::from_env()?;
    let mut body = serde_json::json!({
        "name": args.name,
        "kind": args.kind,
    });
    if let Some(key) = &args.api_key {
        body["api_key"] = serde_json::json!(key);
    }
    if let Some(url) = &args.base_url {
        body["base_url"] = serde_json::json!(url);
    }
    if let Some(model) = &args.model {
        body["default_model"] = serde_json::json!(model);
    }

    match client.add_provider(body).await {
        Ok(result) => {
            println!("  {} Provider '{}' added!", "✓".green().bold(), args.name);
            if let Some(model) = result["default_model"].as_str() {
                println!("  {} Default model: {}", "→".dimmed(), model.cyan());
            }
            println!(
                "  {} Test with: {}",
                "→".dimmed(),
                format!("agentoven provider test {}", args.name).green()
            );
        }
        Err(e) => {
            println!("  {} Failed: {}", "✗".red().bold(), e.to_string().dimmed());
        }
    }
    Ok(())
}

async fn get(args: GetArgs) -> anyhow::Result<()> {
    println!("\n  ⚙️ Provider: {}\n", args.name.bold());

    let client = agentoven_core::AgentOvenClient::from_env()?;
    match client.get_provider(&args.name).await {
        Ok(p) => {
            println!(
                "  {:<16} {}",
                "Name:".bold(),
                p["name"].as_str().unwrap_or("-")
            );
            println!(
                "  {:<16} {}",
                "Kind:".bold(),
                p["kind"].as_str().unwrap_or("-")
            );
            println!(
                "  {:<16} {}",
                "Default Model:".bold(),
                p["default_model"].as_str().unwrap_or("-")
            );
            println!(
                "  {:<16} {}",
                "Base URL:".bold(),
                p["base_url"].as_str().unwrap_or("-")
            );
            let enabled = p["enabled"].as_bool().unwrap_or(true);
            println!(
                "  {:<16} {}",
                "Enabled:".bold(),
                if enabled {
                    "yes".green().to_string()
                } else {
                    "no".red().to_string()
                }
            );
            if let Some(models) = p["discovered_models"].as_array() {
                if !models.is_empty() {
                    println!("\n  {}:", "Discovered Models".bold());
                    for m in models {
                        let id = m["id"].as_str().unwrap_or("-");
                        println!("    {} {}", "•".dimmed(), id.cyan());
                    }
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

async fn update(args: UpdateArgs) -> anyhow::Result<()> {
    println!("\n  ⚙️ Updating provider: {}\n", args.name.bold());

    let client = agentoven_core::AgentOvenClient::from_env()?;
    let mut body = serde_json::json!({});
    if let Some(key) = &args.api_key {
        body["api_key"] = serde_json::json!(key);
    }
    if let Some(url) = &args.base_url {
        body["base_url"] = serde_json::json!(url);
    }
    if let Some(model) = &args.model {
        body["default_model"] = serde_json::json!(model);
    }
    if let Some(enabled) = args.enabled {
        body["enabled"] = serde_json::json!(enabled);
    }

    match client.update_provider(&args.name, body).await {
        Ok(_) => {
            println!("  {} Provider '{}' updated.", "✓".green().bold(), args.name);
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
            .with_prompt(format!("  Remove provider '{}'?", args.name))
            .default(false)
            .interact()?;
        if !confirm {
            println!("  {} Cancelled.", "→".dimmed());
            return Ok(());
        }
    }

    let client = agentoven_core::AgentOvenClient::from_env()?;
    match client.delete_provider(&args.name).await {
        Ok(()) => println!("  {} Provider '{}' removed.", "✓".green().bold(), args.name),
        Err(e) => println!("  {} Failed: {}", "✗".red().bold(), e.to_string().dimmed()),
    }
    Ok(())
}

async fn test(args: TestArgs) -> anyhow::Result<()> {
    println!("\n  🧪 Testing provider: {}...\n", args.name.bold());

    let client = agentoven_core::AgentOvenClient::from_env()?;
    match client.test_provider(&args.name).await {
        Ok(result) => {
            let success = result["success"].as_bool().unwrap_or(false);
            if success {
                println!("  {} Connection successful!", "✓".green().bold());
                if let Some(model) = result["model"].as_str() {
                    println!("  {} Model: {}", "→".dimmed(), model.cyan());
                }
                if let Some(latency) = result["latency_ms"].as_f64() {
                    println!("  {} Latency: {}ms", "→".dimmed(), latency as u64);
                }
            } else {
                let err = result["error"].as_str().unwrap_or("unknown error");
                println!("  {} Test failed: {}", "✗".red().bold(), err);
            }
        }
        Err(e) => {
            println!("  {} Failed: {}", "✗".red().bold(), e.to_string().dimmed());
        }
    }
    Ok(())
}

async fn discover(args: DiscoverArgs) -> anyhow::Result<()> {
    println!("\n  🔍 Discovering models from: {}...\n", args.name.bold());

    let client = agentoven_core::AgentOvenClient::from_env()?;
    match client.discover_provider(&args.name).await {
        Ok(result) => {
            if let Some(models) = result["models"].as_array() {
                if models.is_empty() {
                    println!("  (no models discovered)");
                } else {
                    println!("  {:<30} {:<16}", "MODEL ID".bold(), "CAPABILITIES".bold());
                    println!("  {}", "─".repeat(50).dimmed());
                    for m in models {
                        let id = m["id"].as_str().unwrap_or("-");
                        let caps = m["capabilities"]
                            .as_array()
                            .map(|c| {
                                c.iter()
                                    .filter_map(|v| v.as_str())
                                    .collect::<Vec<_>>()
                                    .join(", ")
                            })
                            .unwrap_or_default();
                        println!("  {:<30} {:<16}", id, caps);
                    }
                    println!("\n  {} {} model(s) found", "→".dimmed(), models.len());
                }
            } else {
                println!(
                    "  {} {}",
                    "→".dimmed(),
                    serde_json::to_string_pretty(&result)?
                );
            }
        }
        Err(e) => {
            println!("  {} Failed: {}", "✗".red().bold(), e.to_string().dimmed());
        }
    }
    Ok(())
}
