//! `agentoven recipe` — manage multi-agent workflows.

use clap::{Args, Subcommand};
use colored::Colorize;
use serde_json;

use agentoven_core::recipe::{Step, StepKind};

#[derive(Subcommand)]
pub enum RecipeCommands {
    /// Create a new recipe.
    Create(CreateArgs),
    /// List all recipes.
    List,
    /// Get recipe details.
    Get(GetArgs),
    /// Delete a recipe.
    Delete(DeleteArgs),
    /// Bake (execute) a recipe.
    Bake(RecipeBakeArgs),
    /// Show recipe execution history / runs.
    Runs(RunsArgs),
    /// Approve a human gate in a recipe run.
    Approve(ApproveArgs),
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
pub struct GetArgs {
    /// Recipe name.
    pub name: String,
}

#[derive(Args)]
pub struct DeleteArgs {
    /// Recipe name.
    pub name: String,
    /// Skip confirmation.
    #[arg(long)]
    pub force: bool,
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
pub struct RunsArgs {
    /// Recipe name.
    pub name: String,
    /// Number of recent runs to show.
    #[arg(long, short, default_value = "10")]
    pub limit: u32,
}

#[derive(Args)]
pub struct ApproveArgs {
    /// Recipe name.
    pub name: String,
    /// Run ID.
    #[arg(long)]
    pub run_id: String,
    /// Gate ID.
    #[arg(long)]
    pub gate_id: String,
    /// Approve or reject.
    #[arg(long, default_value = "true")]
    pub approved: bool,
    /// Comment.
    #[arg(long)]
    pub comment: Option<String>,
}

pub async fn execute(cmd: RecipeCommands) -> anyhow::Result<()> {
    match cmd {
        RecipeCommands::Create(args) => create(args).await,
        RecipeCommands::List => list().await,
        RecipeCommands::Get(args) => get(args).await,
        RecipeCommands::Delete(args) => delete(args).await,
        RecipeCommands::Bake(args) => bake(args).await,
        RecipeCommands::Runs(args) => runs(args).await,
        RecipeCommands::Approve(args) => approve(args).await,
    }
}

async fn create(args: CreateArgs) -> anyhow::Result<()> {
    println!("\n  📖 Creating recipe: {}\n", args.name.bold());

    let client = agentoven_core::AgentOvenClient::from_env()?;

    let steps = if let Some(ref from_path) = args.from {
        let content = tokio::fs::read_to_string(from_path).await?;
        let parsed: serde_json::Value = if from_path.ends_with(".toml") {
            let toml_val: toml::Value = content.parse()?;
            serde_json::to_value(toml_val)?
        } else if from_path.ends_with(".yaml") || from_path.ends_with(".yml") {
            serde_yaml::from_str(&content)?
        } else {
            serde_json::from_str(&content)?
        };

        // Extract steps array from the parsed document
        let steps_arr = parsed
            .get("steps")
            .and_then(|v| v.as_array())
            .ok_or_else(|| anyhow::anyhow!("Missing 'steps' array in {}", from_path))?;

        steps_arr
            .iter()
            .map(|s| {
                let name = s["name"].as_str().unwrap_or("step").to_string();
                let agent = s.get("agent").and_then(|v| v.as_str()).map(String::from);
                let kind_str = s.get("kind").and_then(|v| v.as_str()).unwrap_or("agent");
                let kind = match kind_str {
                    "human-gate" | "gate" => StepKind::HumanGate,
                    "evaluator" => StepKind::Evaluator,
                    "condition" | "branch" => StepKind::Condition,
                    "fan-out" | "parallel" => StepKind::FanOut,
                    "fan-in" | "join" => StepKind::FanIn,
                    _ => StepKind::Agent,
                };
                let depends_on = s
                    .get("depends_on")
                    .and_then(|v| v.as_array())
                    .map(|arr| {
                        arr.iter()
                            .filter_map(|v| v.as_str().map(String::from))
                            .collect()
                    })
                    .unwrap_or_default();
                let timeout = s.get("timeout").and_then(|v| v.as_str()).map(String::from);
                let parallel = s.get("parallel").and_then(|v| v.as_bool()).unwrap_or(false);

                Step {
                    id: uuid::Uuid::new_v4().to_string(),
                    name,
                    kind,
                    agent,
                    parallel,
                    timeout,
                    depends_on,
                    retry: None,
                    notify: Vec::new(),
                    config: None,
                }
            })
            .collect()
    } else {
        Vec::new()
    };

    let recipe = agentoven_core::Recipe::new(&args.name, steps);
    match client.create_recipe(&recipe).await {
        Ok(created) => {
            println!(
                "  {} Recipe '{}' created (ID: {}).",
                "✓".green().bold(),
                args.name,
                created.id.dimmed()
            );
            println!(
                "  {} Execute with: {}",
                "→".dimmed(),
                format!("agentoven recipe bake {}", args.name).green()
            );
        }
        Err(e) => {
            println!(
                "  {} Could not create recipe: {}",
                "⚠".yellow().bold(),
                e.to_string().dimmed()
            );
            println!(
                "  {} Recipe validated locally. ID: {}",
                "✓".green().bold(),
                recipe.id.dimmed()
            );
        }
    }
    Ok(())
}

async fn list() -> anyhow::Result<()> {
    println!("\n  📖 Recipes:\n");

    let client = agentoven_core::AgentOvenClient::from_env()?;
    match client.list_recipes().await {
        Ok(recipes) => {
            if recipes.is_empty() {
                println!("  (no recipes yet — use `agentoven recipe create`)");
            } else {
                println!(
                    "  {:<24} {:<12} {:<8} {:<20}",
                    "NAME".bold(),
                    "STATUS".bold(),
                    "STEPS".bold(),
                    "CREATED".bold()
                );
                println!("  {}", "─".repeat(66).dimmed());
                for r in &recipes {
                    let name = r["name"].as_str().unwrap_or("-");
                    let status = r["status"].as_str().unwrap_or("-");
                    let steps = r["steps"].as_array().map(|a| a.len()).unwrap_or(0);
                    let created = r["created_at"].as_str().unwrap_or("-");
                    let created_short = if created.len() > 16 {
                        &created[..16]
                    } else {
                        created
                    };
                    println!(
                        "  {:<24} {:<12} {:<8} {}",
                        name, status, steps, created_short
                    );
                }
                println!("\n  {} {} recipe(s)", "→".dimmed(), recipes.len());
            }
        }
        Err(e) => {
            println!(
                "  {} Could not list recipes: {}",
                "⚠".yellow().bold(),
                e.to_string().dimmed()
            );
        }
    }
    Ok(())
}

async fn get(args: GetArgs) -> anyhow::Result<()> {
    println!("\n  📖 Recipe: {}\n", args.name.bold());

    let client = agentoven_core::AgentOvenClient::from_env()?;
    match client.get_recipe(&args.name).await {
        Ok(r) => {
            let pretty = serde_json::to_string_pretty(&r).unwrap_or_default();
            for line in pretty.lines() {
                println!("  {}", line.dimmed());
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

async fn delete(args: DeleteArgs) -> anyhow::Result<()> {
    if !args.force {
        let confirm = dialoguer::Confirm::new()
            .with_prompt(format!("  Delete recipe '{}'?", args.name))
            .default(false)
            .interact()?;
        if !confirm {
            println!("  {} Cancelled.", "→".dimmed());
            return Ok(());
        }
    }

    let client = agentoven_core::AgentOvenClient::from_env()?;
    match client.delete_recipe(&args.name).await {
        Ok(()) => println!("  {} Recipe '{}' deleted.", "✓".green().bold(), args.name),
        Err(e) => println!(
            "  {} Delete failed: {}",
            "✗".red().bold(),
            e.to_string().dimmed()
        ),
    }
    Ok(())
}

async fn bake(args: RecipeBakeArgs) -> anyhow::Result<()> {
    println!("\n  🔥 Baking recipe: {}\n", args.name.bold());

    let client = agentoven_core::AgentOvenClient::from_env()?;

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
            if let Some(run_id) = result.get("run_id").or(result.get("task_id")) {
                println!(
                    "  {} Run ID: {}",
                    "→".dimmed(),
                    run_id.as_str().unwrap_or("?").cyan()
                );
            }
            println!(
                "  {} Monitor with: {}",
                "→".dimmed(),
                format!("agentoven recipe runs {}", args.name).green()
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

async fn runs(args: RunsArgs) -> anyhow::Result<()> {
    println!(
        "\n  📊 Runs for recipe: {} (last {})\n",
        args.name.bold(),
        args.limit
    );

    let client = agentoven_core::AgentOvenClient::from_env()?;
    match client.recipe_runs(&args.name).await {
        Ok(runs_list) => {
            if runs_list.is_empty() {
                println!(
                    "  (no runs yet — use `agentoven recipe bake {}` to start)",
                    args.name
                );
            } else {
                println!(
                    "  {:<36} {:<12} {:<12} {:<20}",
                    "RUN ID".bold(),
                    "STATUS".bold(),
                    "DURATION".bold(),
                    "STARTED".bold()
                );
                println!("  {}", "─".repeat(82).dimmed());
                for run in runs_list.iter().take(args.limit as usize) {
                    let id = run["id"].as_str().unwrap_or("-");
                    let status = run["status"].as_str().unwrap_or("-");
                    let duration = run["duration"].as_str().unwrap_or("-");
                    let started = run["started_at"].as_str().unwrap_or("-");
                    let started_short = if started.len() > 16 {
                        &started[..16]
                    } else {
                        started
                    };
                    println!(
                        "  {:<36} {:<12} {:<12} {}",
                        id, status, duration, started_short
                    );
                }
                println!("\n  {} {} run(s)", "→".dimmed(), runs_list.len());
            }
        }
        Err(e) => {
            println!(
                "  {} Could not fetch runs: {}",
                "⚠".yellow().bold(),
                e.to_string().dimmed()
            );
        }
    }
    Ok(())
}

async fn approve(args: ApproveArgs) -> anyhow::Result<()> {
    let action = if args.approved {
        "Approving"
    } else {
        "Rejecting"
    };
    println!(
        "\n  ✅ {} gate {} in run {}...\n",
        action,
        args.gate_id.bold(),
        args.run_id.dimmed()
    );

    let client = agentoven_core::AgentOvenClient::from_env()?;
    match client
        .approve_gate(
            &args.name,
            &args.run_id,
            &args.gate_id,
            args.approved,
            args.comment.as_deref(),
        )
        .await
    {
        Ok(_) => {
            println!(
                "  {} Gate {} {}.",
                "✓".green().bold(),
                args.gate_id,
                if args.approved {
                    "approved"
                } else {
                    "rejected"
                }
            );
        }
        Err(e) => {
            println!("  {} Failed: {}", "✗".red().bold(), e.to_string().dimmed());
        }
    }
    Ok(())
}
