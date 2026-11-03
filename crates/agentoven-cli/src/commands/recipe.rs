//! `agentoven recipe` — manage multi-agent workflows.

use clap::{Args, Subcommand};
use colored::Colorize;
use serde_json;

use agentoven_core::recipe::steps_from_documents;
use agentoven_core::refcheck::Options;
use agentoven_core::AgentOvenClient;

use super::refs;

#[derive(Subcommand)]
pub enum RecipeCommands {
    /// Create a new recipe.
    Create(CreateArgs),
    /// List all recipes.
    List,
    /// Get recipe details.
    Get(GetArgs),
    /// Archive a recipe (its runs are kept).
    Delete(DeleteArgs),
    /// Bake (execute) a recipe.
    Bake(RecipeBakeArgs),
    /// Show recipe execution history / runs.
    Runs(RunsArgs),
    /// Approve a human gate in a recipe run.
    Approve(ApproveArgs),
    /// Check a stored recipe against the kitchen: the agents its steps run, the steps they wait for, the recipes
    /// it nests, its channels, credentials and environment must exist. Prints each problem with the nearest
    /// valid values; exits 1 when any would stop the recipe from running. Asks the control plane (read only).
    Validate(ValidateArgs),
}

#[derive(Args)]
pub struct ValidateArgs {
    /// Recipe name.
    pub name: String,
}

#[derive(Args)]
pub struct CreateArgs {
    /// Recipe name.
    pub name: String,
    /// Path to recipe definition YAML/TOML.
    #[arg(long, short)]
    pub from: Option<String>,
    /// Check the recipe against the kitchen before sending it (the control plane checks it anyway).
    #[arg(long)]
    pub validate: bool,
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
    /// Run on this environment. When it requires approval, a Head Chef or Restaurateur can skip that
    /// approval with --sign (never in a GxP kitchen): a signed decision on the approval_skip gate,
    /// record `<recipe>@<environment>`, bound to this input.
    #[arg(long, alias = "env")]
    pub environment: Option<String>,
    #[command(flatten)]
    pub signing: super::signing::SignArgs,
    /// Check the recipe and the environment against the kitchen before running (the control plane checks them anyway).
    #[arg(long)]
    pub validate: bool,
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
    /// Sign the decision (AgentOven Pro signs every human decision): you are asked for your password.
    #[arg(long)]
    pub sign: bool,
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
        RecipeCommands::Validate(args) => validate(args).await,
    }
}

async fn create(args: CreateArgs) -> anyhow::Result<()> {
    println!("\n  📖 Creating recipe: {}\n", args.name.bold());

    let client = AgentOvenClient::from_env()?;
    create_with(&client, &args).await
}

async fn create_with(client: &AgentOvenClient, args: &CreateArgs) -> anyhow::Result<()> {
    let (description, steps, warnings) = if let Some(ref from_path) = args.from {
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
        let description = parsed
            .get("description")
            .and_then(|v| v.as_str())
            .unwrap_or("")
            .to_string();
        let (steps, warnings) = steps_from_documents(steps_arr)?;
        (description, steps, warnings)
    } else {
        (String::new(), Vec::new(), Vec::new())
    };
    for w in warnings {
        println!("  {} {}", "⚠".yellow().bold(), w.dimmed());
    }
    if args.validate {
        let body = serde_json::json!({"name": args.name, "description": description, "steps": steps});
        refs::gate(client.validate_recipe(&body, &Options::write()).await?)?;
    }

    match client
        .create_recipe_steps(&args.name, &description, steps)
        .await
    {
        Ok(created) => {
            println!(
                "  {} Recipe '{}' created (ID: {}).",
                "✓".green().bold(),
                args.name,
                created
                    .get("id")
                    .and_then(|v| v.as_str())
                    .unwrap_or("-")
                    .dimmed()
            );
            println!(
                "  {} Execute with: {}",
                "→".dimmed(),
                format!("agentoven recipe bake {}", args.name).green()
            );
            Ok(())
        }
        Err(e) => {
            if let Some(refused) = refs::refused(&e) {
                return Err(refused);
            }
            println!(
                "  {} Could not create recipe: {}",
                "✗".red().bold(),
                e.to_string().dimmed()
            );
            Err(e)
        }
    }
}

async fn validate(args: ValidateArgs) -> anyhow::Result<()> {
    let client = AgentOvenClient::from_env()?;
    let (text, code) = validate_with(&client, &args.name).await?;
    print!("{text}");
    if code != 0 {
        std::process::exit(code);
    }
    Ok(())
}

/// The report of `recipe validate` and its exit code, free of printing and `process::exit` for tests.
async fn validate_with(client: &AgentOvenClient, name: &str) -> anyhow::Result<(String, i32)> {
    let verdict = client.recipe_validate(name).await?;
    Ok(refs::render_validation("recipe", &verdict))
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
            .with_prompt(format!(
                "  Archive recipe '{}'? Its runs are kept.",
                args.name
            ))
            .default(false)
            .interact()?;
        if !confirm {
            println!("  {} Cancelled.", "→".dimmed());
            return Ok(());
        }
    }

    let client = agentoven_core::AgentOvenClient::from_env()?;
    match client.delete_recipe(&args.name).await {
        Ok(()) => println!(
            "  {} Recipe '{}' archived (its runs are kept).",
            "✓".green().bold(),
            args.name
        ),
        Err(e) => println!(
            "  {} Archive failed: {}",
            "✗".red().bold(),
            e.to_string().dimmed()
        ),
    }
    Ok(())
}

async fn bake(args: RecipeBakeArgs) -> anyhow::Result<()> {
    println!("\n  🔥 Baking recipe: {}\n", args.name.bold());

    let client = AgentOvenClient::from_env()?;
    bake_with(&client, &args).await
}

async fn bake_with(client: &AgentOvenClient, args: &RecipeBakeArgs) -> anyhow::Result<()> {
    let input = if let Some(ref json_str) = args.input {
        serde_json::from_str(json_str)?
    } else if let Some(ref file_path) = args.input_file {
        let content = tokio::fs::read_to_string(file_path).await?;
        serde_json::from_str(&content)?
    } else {
        serde_json::json!({})
    };

    if args.validate {
        refs::gate(client.validate_stored_recipe(&args.name, args.environment.as_deref().unwrap_or_default()).await?)?;
    }
    let signing = args.signing.resolve()?;
    let started = if args.environment.is_some() || !matches!(signing, super::signing::Signing::None) {
        client
            .run_recipe(&args.name, &input, args.environment.as_deref(), signing.signature())
            .await
    } else {
        client.bake_recipe(&args.name, input).await
    };
    match started {
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
            if let Some(refused) = refs::refused(&e) {
                return Err(refused);
            }
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
    let token = if args.sign {
        let password = dialoguer::Password::new()
            .with_prompt("  Password (to sign this decision)")
            .interact()?;
        let meaning = if args.approved {
            "approved"
        } else {
            "rejected"
        };
        let record = format!("{}:{}", args.run_id, args.gate_id);
        match client
            .sign_decision("recipe_gate", &record, meaning, &password)
            .await
        {
            Ok(t) => Some(t),
            Err(e) => {
                println!(
                    "  {} Not signed: {}",
                    "✗".red().bold(),
                    e.to_string().dimmed()
                );
                return Ok(());
            }
        }
    } else {
        None
    };
    match client
        .approve_gate_signed(
            &args.name,
            &args.run_id,
            &args.gate_id,
            args.approved,
            args.comment.as_deref(),
            token.as_deref(),
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
            if !args.sign && e.to_string().contains("signature_required") {
                println!(
                    "  {} AgentOven Pro signs every decision: run it again with --sign",
                    "→".bold()
                );
            }
        }
    }
    Ok(())
}

#[cfg(test)]
mod tests {
    use super::*;
    use crate::commands::{Cli, Commands};
    use agentoven_core::testing::{fake_control_plane, json as reply, Log, Reply, Routes};
    use clap::Parser;
    use serde_json::json;

    fn parse(args: &[&str]) -> RecipeCommands {
        let mut v = vec!["agentoven", "recipe"];
        v.extend_from_slice(args);
        match Cli::try_parse_from(v).expect("parse").command {
            Commands::Recipe(c) => c,
            _ => panic!("not a recipe command"),
        }
    }

    async fn serve(routes: Vec<(&str, Reply)>) -> (AgentOvenClient, Log) {
        colored::control::set_override(false);
        let mut r = Routes::new();
        for (k, v) in routes {
            r.insert(k.to_string(), v);
        }
        let (base, log) = fake_control_plane(r).await;
        (AgentOvenClient::new(&base).unwrap().with_kitchen("acme"), log)
    }

    /// The lists `--validate` reads: one agent called writer and one environment, prod.
    fn lists() -> Vec<(&'static str, Reply)> {
        vec![
            ("GET /api/v1/models/providers", reply(200, json!([]))),
            ("GET /api/v1/prompts", reply(200, json!([]))),
            ("GET /api/v1/tools", reply(200, json!([]))),
            ("GET /api/v1/skills", reply(200, json!([]))),
            ("GET /api/v1/agents", reply(200, json!([{"name": "writer", "status": "ready"}]))),
            ("GET /api/v1/recipes", reply(200, json!([]))),
            ("GET /api/v1/channels", reply(200, json!([]))),
            ("GET /api/v1/environments", reply(200, json!([{"slug": "prod"}]))),
            ("GET /api/v1/credentials", reply(200, json!([]))),
            ("GET /api/v1/scenarios", reply(404, json!({}))),
        ]
    }

    fn writes(log: &Log) -> Vec<String> {
        log.calls().into_iter().filter(|c| !c.starts_with("GET ")).collect()
    }

    #[test]
    fn validate_flags_parse_and_default_to_off() {
        assert!(matches!(parse(&["validate", "refunds"]), RecipeCommands::Validate(a) if a.name == "refunds"));
        assert!(matches!(parse(&["create", "refunds"]), RecipeCommands::Create(a) if !a.validate));
        assert!(matches!(parse(&["create", "refunds", "--validate"]), RecipeCommands::Create(a) if a.validate));
        assert!(matches!(parse(&["bake", "refunds", "--validate", "--env", "prod"]), RecipeCommands::Bake(a) if a.validate));
    }

    #[tokio::test]
    async fn recipe_validate_reports_a_step_that_names_no_agent() {
        let verdict = json!({"recipe": "refunds", "valid": false, "warnings": [], "problems": [{
            "path": "steps[0].agent_ref", "code": "not_found", "severity": "error", "kind": "agent", "value": "writr",
            "message": "steps[0].agent_ref: \"writr\" is not an agent in this kitchen (did you mean \"writer\"?)", "suggestions": ["writer"]}]});
        let (client, _) = serve(vec![("GET /api/v1/recipes/refunds/validate", reply(200, verdict))]).await;
        let (text, code) = validate_with(&client, "refunds").await.unwrap();
        assert_eq!(code, 1);
        assert!(text.contains("steps[0].agent_ref = \"writr\" [agent]") && text.contains("did you mean"), "{text}");
    }

    #[tokio::test]
    async fn create_with_validate_refuses_before_sending_and_without_it_the_server_decides() {
        let mut routes = lists();
        routes.push(("POST /api/v1/recipes", reply(201, json!({"id": "r1"}))));
        let (client, log) = serve(routes).await;
        let dir = tempfile::tempdir().unwrap();
        let file = dir.path().join("recipe.json");
        std::fs::write(&file, r#"{"steps": [{"name": "a", "kind": "agent", "agent": "writr"}]}"#).unwrap();
        let args = CreateArgs { name: "refunds".into(), from: Some(file.to_string_lossy().into()), validate: true };
        let err = create_with(&client, &args).await.unwrap_err();
        assert!(err.to_string().contains("names things this kitchen does not have"), "{err}");
        assert!(writes(&log).is_empty(), "{:?}", log.calls());
        // Off: the recipe is sent and the server answers.
        create_with(&client, &CreateArgs { validate: false, ..args }).await.unwrap();
        assert_eq!(writes(&log), vec!["POST /api/v1/recipes"]);
    }

    #[tokio::test]
    async fn bake_with_validate_checks_the_environment_before_running() {
        let mut routes = lists();
        routes.push(("GET /api/v1/recipes/refunds", reply(200, json!({"name": "refunds", "steps": [{"name": "a", "kind": "agent", "agent_ref": "writer"}]}))));
        routes.push(("POST /api/v1/recipes/refunds/bake", reply(200, json!({"run_id": "run1"}))));
        let (client, log) = serve(routes).await;
        let args = |env: &str| RecipeBakeArgs {
            name: "refunds".into(),
            input: None,
            input_file: None,
            environment: Some(env.into()),
            signing: crate::commands::signing::SignArgs::default(),
            validate: true,
        };
        let err = bake_with(&client, &args("prd")).await.unwrap_err();
        assert!(err.to_string().contains("1 problem"), "{err}");
        assert!(writes(&log).is_empty());
        bake_with(&client, &args("prod")).await.unwrap();
        assert_eq!(writes(&log), vec!["POST /api/v1/recipes/refunds/bake"]);
    }
}
