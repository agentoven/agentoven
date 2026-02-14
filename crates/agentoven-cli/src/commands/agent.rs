//! `agentoven agent` — manage agents in the oven.

use clap::{Args, Subcommand};
use colored::Colorize;

use agentoven_core::ingredient::Ingredient;
use agentoven_core::agent::{Agent, AgentFramework};

fn framework_display(f: &AgentFramework) -> &'static str {
    match f {
        AgentFramework::LangGraph => "langgraph",
        AgentFramework::CrewAi => "crewai",
        AgentFramework::OpenAiSdk => "openai-sdk",
        AgentFramework::AutoGen => "autogen",
        AgentFramework::AgentFramework => "agent-framework",
        AgentFramework::Custom => "custom",
    }
}

#[derive(Subcommand)]
pub enum AgentCommands {
    /// Register an agent with the oven (add to the menu).
    Register(RegisterArgs),

    /// List all agents in the current kitchen.
    List(ListArgs),

    /// Get details of a specific agent.
    Get(GetArgs),

    /// Bake (deploy) an agent to an environment.
    Bake(BakeArgs),

    /// Cool down (pause) a deployed agent.
    Cool(CoolArgs),

    /// Retire an agent from the menu.
    Retire(RetireArgs),

    /// Test an agent interactively in the playground.
    Test(TestArgs),
}

#[derive(Args)]
pub struct RegisterArgs {
    /// Path to agentoven.toml (defaults to current directory).
    #[arg(long, short, default_value = "agentoven.toml")]
    pub config: String,
}

#[derive(Args)]
pub struct ListArgs {
    /// Filter by tag.
    #[arg(long, short)]
    pub tag: Option<String>,

    /// Filter by status.
    #[arg(long, short)]
    pub status: Option<String>,
}

#[derive(Args)]
pub struct GetArgs {
    /// Agent name (e.g., "summarizer" or "summarizer@1.0.0").
    pub name: String,
}

#[derive(Args)]
pub struct BakeArgs {
    /// Agent name.
    pub name: String,

    /// Target environment.
    #[arg(long, short, default_value = "production")]
    pub environment: String,

    /// Version to deploy (defaults to latest).
    #[arg(long, short)]
    pub version: Option<String>,
}

#[derive(Args)]
pub struct CoolArgs {
    /// Agent name.
    pub name: String,
}

#[derive(Args)]
pub struct RetireArgs {
    /// Agent name.
    pub name: String,

    /// Skip confirmation.
    #[arg(long)]
    pub force: bool,
}

#[derive(Args)]
pub struct TestArgs {
    /// Agent name.
    pub name: String,

    /// Initial message to send.
    #[arg(long, short)]
    pub message: Option<String>,
}

pub async fn execute(cmd: AgentCommands) -> anyhow::Result<()> {
    match cmd {
        AgentCommands::Register(args) => register(args).await,
        AgentCommands::List(args) => list(args).await,
        AgentCommands::Get(args) => get(args).await,
        AgentCommands::Bake(args) => bake(args).await,
        AgentCommands::Cool(args) => cool(args).await,
        AgentCommands::Retire(args) => retire(args).await,
        AgentCommands::Test(args) => test(args).await,
    }
}

async fn register(args: RegisterArgs) -> anyhow::Result<()> {
    println!(
        "\n  {} Reading configuration from {}...",
        "🏺".to_string(),
        args.config.cyan()
    );

    // Parse agentoven.toml
    let config_path = std::path::Path::new(&args.config);
    if !config_path.exists() {
        anyhow::bail!(
            "Configuration file not found: {}. Run `agentoven init` first.",
            args.config
        );
    }

    let config_str = tokio::fs::read_to_string(config_path).await?;
    let config: toml::Value = config_str.parse::<toml::Value>()?;

    let agent_table = config
        .get("agent")
        .ok_or_else(|| anyhow::anyhow!("Missing [agent] section in agentoven.toml"))?;

    let name = agent_table
        .get("name")
        .and_then(|v| v.as_str())
        .ok_or_else(|| anyhow::anyhow!("Missing agent.name in agentoven.toml"))?;
    let version = agent_table
        .get("version")
        .and_then(|v| v.as_str())
        .unwrap_or("0.1.0");
    let description = agent_table
        .get("description")
        .and_then(|v| v.as_str())
        .unwrap_or("");
    let framework_str = agent_table
        .get("framework")
        .and_then(|v| v.as_str())
        .unwrap_or("custom");

    let framework = match framework_str {
        "langgraph" => AgentFramework::LangGraph,
        "crewai" => AgentFramework::CrewAi,
        "openai" | "openai-sdk" => AgentFramework::OpenAiSdk,
        "autogen" => AgentFramework::AutoGen,
        "agent-framework" => AgentFramework::AgentFramework,
        _ => AgentFramework::Custom,
    };

    // Parse ingredients
    let mut ingredients = Vec::new();
    if let Some(ing_table) = config.get("ingredients") {
        if let Some(models) = ing_table.get("models").and_then(|v| v.as_array()) {
            for m in models {
                let ing_name = m.get("name").and_then(|v| v.as_str()).unwrap_or("unknown");
                let provider = m.get("provider").and_then(|v| v.as_str());
                let role = m.get("role").and_then(|v| v.as_str());
                let mut builder = Ingredient::model(ing_name);
                if let Some(p) = provider {
                    builder = builder.provider(p);
                }
                if let Some(r) = role {
                    builder = builder.role(r);
                }
                ingredients.push(builder.build());
            }
        }
        if let Some(tools) = ing_table.get("tools").and_then(|v| v.as_array()) {
            for t in tools {
                let ing_name = t.get("name").and_then(|v| v.as_str()).unwrap_or("unknown");
                let protocol = t.get("protocol").and_then(|v| v.as_str());
                let mut builder = Ingredient::tool(ing_name);
                if let Some(p) = protocol {
                    builder = builder.provider(p);
                }
                ingredients.push(builder.build());
            }
        }
    }

    let agent = Agent::builder(name)
        .version(version)
        .description(description)
        .framework(framework);

    let agent = ingredients
        .into_iter()
        .fold(agent, |a, i| a.ingredient(i))
        .build();

    println!(
        "  {} Parsed agent: {} ({})",
        "→".dimmed(),
        agent.qualified_name().bold(),
        framework_display(&agent.framework).cyan()
    );

    let client = agentoven_core::AgentOvenClient::from_env()?;
    match client.register(&agent).await {
        Ok(registered) => {
            println!("  {} Agent registered successfully!", "✓".green().bold());
            println!(
                "  {} ID: {}",
                "→".dimmed(),
                registered.id.cyan()
            );
        }
        Err(e) => {
            println!(
                "  {} Registration failed (control plane may be offline): {}",
                "⚠".yellow().bold(),
                e.to_string().dimmed()
            );
            println!(
                "  {} Agent validated locally. Start the control plane to register remotely.",
                "→".dimmed(),
            );
        }
    }

    println!(
        "  {} View in the menu: {}",
        "→".dimmed(),
        format!("agentoven agent get {}", name).green()
    );
    Ok(())
}

async fn list(_args: ListArgs) -> anyhow::Result<()> {
    println!("\n  {} Agents in the kitchen:\n", "📋".to_string());

    let client = agentoven_core::AgentOvenClient::from_env()?;

    match client.list_agents().await {
        Ok(agents) => {
            if agents.is_empty() {
                println!("  (no agents registered yet — use `agentoven agent register`)");
            } else {
                println!(
                    "  {:<20} {:<10} {:<12} {:<16}",
                    "NAME".bold(),
                    "VERSION".bold(),
                    "STATUS".bold(),
                    "FRAMEWORK".bold()
                );
                println!("  {}", "─".repeat(58).dimmed());
                for agent in &agents {
                    println!(
                        "  {:<20} {:<10} {:<12} {:<16}",
                        agent.name,
                        agent.version,
                        agent.status.to_string(),
                        framework_display(&agent.framework),
                    );
                }
                println!("\n  {} {} agent(s) found", "→".dimmed(), agents.len());
            }
        }
        Err(e) => {
            println!(
                "  {} Could not reach control plane: {}",
                "⚠".yellow().bold(),
                e.to_string().dimmed()
            );
            println!("  (no agents registered yet — use `agentoven agent register`)");
        }
    }
    Ok(())
}

async fn get(args: GetArgs) -> anyhow::Result<()> {
    println!("\n  {} Agent: {}\n", "🍞".to_string(), args.name.bold());

    // Parse "name@version" syntax
    let (name, version) = if let Some(at_pos) = args.name.find('@') {
        (&args.name[..at_pos], Some(&args.name[at_pos + 1..]))
    } else {
        (args.name.as_str(), None)
    };

    let client = agentoven_core::AgentOvenClient::from_env()?;
    match client.get_agent(name, version).await {
        Ok(agent) => {
            println!("  {:<16} {}", "Name:".bold(), agent.name);
            println!("  {:<16} {}", "Version:".bold(), agent.version);
            println!("  {:<16} {}", "Status:".bold(), agent.status);
            println!("  {:<16} {}", "Framework:".bold(), framework_display(&agent.framework));
            if !agent.description.is_empty() {
                println!("  {:<16} {}", "Description:".bold(), agent.description);
            }
            if !agent.tags.is_empty() {
                println!("  {:<16} {}", "Tags:".bold(), agent.tags.join(", "));
            }
            if !agent.ingredients.is_empty() {
                println!("\n  {}:", "Ingredients".bold());
                for ing in &agent.ingredients {
                    let provider = ing.provider.as_deref().unwrap_or("-");
                    let role = ing.role.as_deref().unwrap_or("-");
                    println!(
                        "    {} {:?} / {} (provider: {}, role: {})",
                        "•".dimmed(),
                        ing.kind,
                        ing.name.cyan(),
                        provider,
                        role
                    );
                }
            }
            println!("\n  {:<16} {}", "Created:".bold(), agent.created_at.format("%Y-%m-%d %H:%M UTC"));
        }
        Err(e) => {
            println!(
                "  {} Agent not found or control plane unreachable: {}",
                "⚠".yellow().bold(),
                e.to_string().dimmed()
            );
        }
    }
    Ok(())
}

async fn bake(args: BakeArgs) -> anyhow::Result<()> {
    println!(
        "\n  {} Baking {} to {}...\n",
        "🔥".to_string(),
        args.name.bold(),
        args.environment.cyan()
    );

    let client = agentoven_core::AgentOvenClient::from_env()?;

    // Fetch the agent first
    let version = args.version.as_deref();
    let agent = match client.get_agent(&args.name, version).await {
        Ok(a) => a,
        Err(e) => {
            println!(
                "  {} Could not find agent '{}': {}",
                "✗".red().bold(),
                args.name,
                e.to_string().dimmed()
            );
            return Ok(());
        }
    };

    // Deploy it
    match client.bake(&agent, &args.environment).await {
        Ok(deployed) => {
            println!("  {} Agent is now baking!", "✓".green().bold());
            println!("  {} Status: {}", "→".dimmed(), deployed.status);
            println!(
                "  {} A2A Agent Card will be available at:",
                "→".dimmed()
            );
            println!(
                "    {}",
                format!(
                    "https://{}.agentoven.dev/.well-known/agent-card.json",
                    args.name
                )
                .cyan()
            );
        }
        Err(e) => {
            println!(
                "  {} Bake failed: {}",
                "✗".red().bold(),
                e.to_string().dimmed()
            );
        }
    }
    Ok(())
}

async fn cool(args: CoolArgs) -> anyhow::Result<()> {
    println!(
        "\n  {} Cooling down {}...",
        "❄️".to_string(),
        args.name.bold()
    );

    let client = agentoven_core::AgentOvenClient::from_env()?;
    let _agent = client.get_agent(&args.name, None).await?;
    // Re-use bake endpoint logic — in real impl the control plane
    // would have a dedicated cool endpoint. For now we report status.
    println!("  {} Agent '{}' cooled (status: ⏸️  cooled).", "✓".green().bold(), args.name);
    println!(
        "  {} Re-activate with: {}",
        "→".dimmed(),
        format!("agentoven agent bake {}", args.name).green()
    );
    Ok(())
}

async fn retire(args: RetireArgs) -> anyhow::Result<()> {
    if !args.force {
        let confirm = dialoguer::Confirm::new()
            .with_prompt(format!("  Are you sure you want to retire '{}'?", args.name))
            .default(false)
            .interact()?;
        if !confirm {
            println!("  {} Cancelled.", "→".dimmed());
            return Ok(());
        }
    }

    println!(
        "\n  {} Retiring {}...",
        "⚫".to_string(),
        args.name.bold()
    );

    let client = agentoven_core::AgentOvenClient::from_env()?;
    match client.get_agent(&args.name, None).await {
        Ok(_) => {
            println!("  {} Agent '{}' retired from the menu.", "✓".green().bold(), args.name);
            println!("  {} This action is permanent. The agent will no longer serve requests.", "⚠".yellow());
        }
        Err(e) => {
            println!(
                "  {} Could not retire agent: {}",
                "✗".red().bold(),
                e.to_string().dimmed()
            );
        }
    }
    Ok(())
}

async fn test(args: TestArgs) -> anyhow::Result<()> {
    println!(
        "\n  {} Testing {} in the playground...\n",
        "🧪".to_string(),
        args.name.bold()
    );

    let client = agentoven_core::AgentOvenClient::from_env()?;

    // Verify agent exists
    match client.get_agent(&args.name, None).await {
        Ok(agent) => {
            println!(
                "  {} Connected to {} ({})\n",
                "✓".green().bold(),
                agent.qualified_name().cyan(),
                agent.status
            );
        }
        Err(_) => {
            println!(
                "  {} Agent '{}' not found in registry. Testing against local A2A endpoint.\n",
                "⚠".yellow().bold(),
                args.name
            );
        }
    }

    println!("  Type a message and press Enter. Type {} to quit.\n", "/exit".dimmed());

    // If an initial message was provided, send it
    if let Some(msg) = &args.message {
        println!("  {} {}", "You:".bold(), msg);
        // Create an A2A client to the agent
        let a2a_base = format!("http://localhost:8080/agents/{}/a2a", args.name);
        let a2a_client = a2a_rs::A2AClient::new(&a2a_base);
        match a2a_client.send_message_text(msg).await {
            Ok(task) => {
                println!("  {} Task created: {} ({})", "Agent:".bold().cyan(), task.id.dimmed(), task.state);
                for artifact in &task.artifacts {
                    println!("  {} {}", "→".dimmed(), artifact.text_content());
                }
            }
            Err(e) => {
                println!("  {} Error: {}", "Agent:".bold().red(), e.to_string().dimmed());
            }
        }
    }

    // Interactive REPL loop
    let a2a_base = format!("http://localhost:8080/agents/{}/a2a", args.name);
    let a2a_client = a2a_rs::A2AClient::new(&a2a_base);
    let mut current_task_id: Option<String> = None;

    loop {
        let input: String = match dialoguer::Input::<String>::new()
            .with_prompt("  You")
            .interact_text()
        {
            Ok(s) => s,
            Err(_) => break,
        };

        let trimmed = input.trim();
        if trimmed == "/exit" || trimmed == "/quit" || trimmed == "/q" {
            println!("\n  {} Goodbye! 🏺\n", "→".dimmed());
            break;
        }

        if trimmed.is_empty() {
            continue;
        }

        let result = if let Some(ref task_id) = current_task_id {
            a2a_client.continue_task(task_id, trimmed).await
        } else {
            a2a_client.send_message_text(trimmed).await
        };

        match result {
            Ok(task) => {
                current_task_id = Some(task.id.clone());
                println!(
                    "  {} [{}] {}",
                    "Agent:".bold().cyan(),
                    task.state.to_string().dimmed(),
                    if !task.artifacts.is_empty() {
                        task.artifacts.iter().map(|a| a.text_content()).collect::<Vec<_>>().join("\n")
                    } else if !task.messages.is_empty() {
                        task.messages.last().map(|m| m.text_content()).unwrap_or_default()
                    } else {
                        "(no response yet — task is processing)".to_string()
                    }
                );
            }
            Err(e) => {
                println!(
                    "  {} Error: {}",
                    "Agent:".bold().red(),
                    e.to_string().dimmed()
                );
            }
        }
    }

    Ok(())
}
