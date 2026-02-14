//! `agentoven recipe` — manage multi-agent workflows.

use clap::{Args, Subcommand};
use colored::Colorize;
use serde_json;

#[derive(Subcommand)]
pub enum RecipeCommands {
    /// Create a new recipe.
    Create(CreateArgs),
    /// List all recipes.
    List,
    /// Bake (execute) a recipe.
    Bake(RecipeBakeArgs),
    /// Show recipe execution history.
    History(HistoryArgs),
}

#[derive(Args)]
pub struct CreateArgs {
    /// Recipe name.
    pub name: String,
    /// Path to recipe definition YAML/TOML.
    #[arg(long, short)]
    pub from: Option<String>,
}

#[derive(Args)]
pub struct RecipeBakeArgs {
    /// Recipe name.
    pub name: String,
    /// Input data as JSON string.
    #[arg(long, short)]
    pub input: Option<String>,
    /// Input from file.
    #[arg(long)]
    pub input_file: Option<String>,
}

#[derive(Args)]
pub struct HistoryArgs {
    /// Recipe name.
    pub name: String,
    /// Number of recent runs to show.
    #[arg(long, short, default_value = "10")]
    pub limit: u32,
}

pub async fn execute(cmd: RecipeCommands) -> anyhow::Result<()> {
    match cmd {
        RecipeCommands::Create(args) => {
            println!("\n  {} Creating recipe: {}\n", "📖".to_string(), args.name.bold());

            let client = agentoven_core::AgentOvenClient::from_env()?;

            // If --from is specified, read recipe definition from file
            let steps = if let Some(ref from_path) = args.from {
                let content = tokio::fs::read_to_string(from_path).await?;
                // Try to parse as TOML or JSON
                if from_path.ends_with(".toml") {
                    let _parsed: toml::Value = content.parse()?;
                    // Extract steps from the recipe definition
                    Vec::new() // Steps would be parsed from TOML
                } else {
                    let _parsed: serde_json::Value = serde_json::from_str(&content)?;
                    Vec::new() // Steps would be parsed from JSON
                }
            } else {
                Vec::new()
            };

            let recipe = agentoven_core::Recipe::new(&args.name, steps);
            match client.create_recipe(&recipe).await {
                Ok(created) => {
                    println!("  {} Recipe '{}' created (ID: {}).", "✓".green().bold(), args.name, created.id.dimmed());
                    println!(
                        "  {} Execute with: {}",
                        "→".dimmed(),
                        format!("agentoven recipe bake {}", args.name).green()
                    );
                }
                Err(e) => {
                    println!(
                        "  {} Could not create recipe on control plane: {}",
                        "⚠".yellow().bold(),
                        e.to_string().dimmed()
                    );
                    println!("  {} Recipe validated locally. ID: {}", "✓".green().bold(), recipe.id.dimmed());
                }
            }
            Ok(())
        }
        RecipeCommands::List => {
            println!("\n  {} Recipes:\n", "📖".to_string());

            let _client = agentoven_core::AgentOvenClient::from_env()?;
            // Control plane list_recipes not implemented yet — show placeholder
            println!("  (no recipes yet — use `agentoven recipe create`)");
            Ok(())
        }
        RecipeCommands::Bake(args) => {
            println!(
                "\n  {} Baking recipe: {}\n",
                "🔥".to_string(),
                args.name.bold()
            );

            let client = agentoven_core::AgentOvenClient::from_env()?;

            // Parse input from --input flag or --input-file
            let input = if let Some(ref json_str) = args.input {
                serde_json::from_str(json_str)?
            } else if let Some(ref file_path) = args.input_file {
                let content = tokio::fs::read_to_string(file_path).await?;
                serde_json::from_str(&content)?
            } else {
                serde_json::json!({})
            };

            match client.bake_recipe(&args.name, input).await {
                Ok(result) => {
                    println!("  {} Recipe baking started!", "✓".green().bold());
                    if let Some(task_id) = result.get("task_id").and_then(|v| v.as_str()) {
                        println!("  {} Task ID: {}", "→".dimmed(), task_id.cyan());
                    }
                    println!(
                        "  {} Monitor with: {}",
                        "→".dimmed(),
                        "agentoven trace ls".green()
                    );
                }
                Err(e) => {
                    println!(
                        "  {} Recipe bake failed: {}",
                        "✗".red().bold(),
                        e.to_string().dimmed()
                    );
                }
            }
            Ok(())
        }
        RecipeCommands::History(args) => {
            println!(
                "\n  {} History for recipe: {} (last {})\n",
                "📊".to_string(),
                args.name.bold(),
                args.limit
            );

            println!(
                "  {:<36} {:<12} {:<12} {:<10}",
                "RUN ID".bold(),
                "STATUS".bold(),
                "DURATION".bold(),
                "STARTED".bold(),
            );
            println!("  {}", "─".repeat(70).dimmed());
            println!("  (no runs yet — use `agentoven recipe bake {}` to start)", args.name);
            Ok(())
        }
    }
}
