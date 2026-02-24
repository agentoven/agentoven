//! `agentoven agent` — manage agents in the oven.

use clap::{Args, Subcommand};
use colored::Colorize;

use agentoven_core::agent::{Agent, AgentFramework, AgentMode};
use agentoven_core::ingredient::Ingredient;

fn framework_display(f: &AgentFramework) -> &'static str {
    match f {
        AgentFramework::LangGraph => "langgraph",
        AgentFramework::CrewAi => "crewai",
        AgentFramework::OpenAiSdk => "openai-sdk",
        AgentFramework::AutoGen => "autogen",
        AgentFramework::AgentFramework => "agent-framework",
        AgentFramework::Managed => "managed",
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

    /// Update an existing agent's configuration.
    Update(UpdateArgs),

    /// Delete an agent from the menu.
    Delete(DeleteArgs),

    /// Bake (deploy) an agent to an environment.
    Bake(BakeArgs),

    /// Re-cook an agent with edits (hot-swap config).
    Recook(RecookArgs),

    /// Cool down (pause) a deployed agent.
    Cool(CoolArgs),

    /// Rewarm a cooled agent (bring it back to ready).
    Rewarm(RewarmArgs),

    /// Retire an agent from the menu (permanent).
    Retire(RetireArgs),

    /// Test an agent (one-shot or interactive playground).
    Test(TestArgs),

    /// Invoke a managed agent (full agentic loop with execution trace).
    Invoke(InvokeArgs),

    /// Show resolved configuration for a baked agent.
    Config(ConfigArgs),

    /// Show the A2A Agent Card (discovery metadata).
    Card(CardArgs),

    /// List version history for an agent.
    Versions(VersionsArgs),
}

#[derive(Args)]
pub struct RegisterArgs {
    /// Agent name (optional if using --config).
    pub name: Option<String>,

    /// Path to agentoven.toml.
    #[arg(long, short)]
    pub config: Option<String>,

    // ── Direct flags (used when not using --config) ──
    /// Agent description.
    #[arg(long)]
    pub description: Option<String>,

    /// Framework (langgraph, crewai, openai-sdk, autogen, managed, custom).
    #[arg(long)]
    pub framework: Option<String>,

    /// Agent mode: managed (built-in executor) or external (A2A proxy).
    #[arg(long, default_value = "managed")]
    pub mode: String,

    /// Primary model provider name.
    #[arg(long)]
    pub model_provider: Option<String>,

    /// Primary model name.
    #[arg(long)]
    pub model_name: Option<String>,

    /// Backup model provider for failover.
    #[arg(long)]
    pub backup_provider: Option<String>,

    /// Backup model name for failover.
    #[arg(long)]
    pub backup_model: Option<String>,

    /// System prompt / instructions.
    #[arg(long)]
    pub system_prompt: Option<String>,

    /// Maximum turns for managed agentic loop.
    #[arg(long)]
    pub max_turns: Option<u32>,

    /// Agent skills (repeatable).
    #[arg(long)]
    pub skill: Vec<String>,

    /// Tags (repeatable).
    #[arg(long)]
    pub tag: Vec<String>,

    /// A2A endpoint for external agents.
    #[arg(long)]
    pub a2a_endpoint: Option<String>,

    /// Guardrail in kind:stage format (e.g., "content-filter:pre"). Repeatable.
    #[arg(long)]
    pub guardrail: Vec<String>,
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
pub struct UpdateArgs {
    /// Agent name.
    pub name: String,

    /// New description.
    #[arg(long)]
    pub description: Option<String>,

    /// New model provider.
    #[arg(long)]
    pub model_provider: Option<String>,

    /// New model name.
    #[arg(long)]
    pub model_name: Option<String>,

    /// New system prompt.
    #[arg(long)]
    pub system_prompt: Option<String>,

    /// New max turns.
    #[arg(long)]
    pub max_turns: Option<u32>,

    /// New backup provider.
    #[arg(long)]
    pub backup_provider: Option<String>,

    /// New backup model.
    #[arg(long)]
    pub backup_model: Option<String>,

    /// Raw JSON update body (overrides other flags).
    #[arg(long)]
    pub json: Option<String>,
}

#[derive(Args)]
pub struct DeleteArgs {
    /// Agent name.
    pub name: String,
    /// Skip confirmation.
    #[arg(long)]
    pub force: bool,
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
pub struct RecookArgs {
    /// Agent name.
    pub name: String,

    /// New model provider.
    #[arg(long)]
    pub model_provider: Option<String>,

    /// New model name.
    #[arg(long)]
    pub model_name: Option<String>,

    /// New system prompt.
    #[arg(long)]
    pub system_prompt: Option<String>,

    /// Raw JSON edits body.
    #[arg(long)]
    pub json: Option<String>,
}

#[derive(Args)]
pub struct CoolArgs {
    /// Agent name.
    pub name: String,
}

#[derive(Args)]
pub struct RewarmArgs {
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

    /// Message to test (one-shot mode via /test endpoint).
    #[arg(long, short)]
    pub message: Option<String>,

    /// Enable thinking / chain-of-thought.
    #[arg(long)]
    pub thinking: bool,

    /// Use interactive A2A playground (default when no --message).
    #[arg(long)]
    pub interactive: bool,
}

#[derive(Args)]
pub struct InvokeArgs {
    /// Agent name.
    pub name: String,

    /// Message to send.
    #[arg(long, short)]
    pub message: String,

    /// Variables as JSON object.
    #[arg(long)]
    pub variables: Option<String>,

    /// Enable thinking / chain-of-thought.
    #[arg(long)]
    pub thinking: bool,
}

#[derive(Args)]
pub struct ConfigArgs {
    /// Agent name.
    pub name: String,
}

#[derive(Args)]
pub struct CardArgs {
    /// Agent name.
    pub name: String,
}

#[derive(Args)]
pub struct VersionsArgs {
    /// Agent name.
    pub name: String,
}

pub async fn execute(cmd: AgentCommands) -> anyhow::Result<()> {
    match cmd {
        AgentCommands::Register(args) => register(args).await,
        AgentCommands::List(args) => list(args).await,
        AgentCommands::Get(args) => get(args).await,
        AgentCommands::Update(args) => update(args).await,
        AgentCommands::Delete(args) => delete(args).await,
        AgentCommands::Bake(args) => bake(args).await,
        AgentCommands::Recook(args) => recook(args).await,
        AgentCommands::Cool(args) => cool(args).await,
        AgentCommands::Rewarm(args) => rewarm(args).await,
        AgentCommands::Retire(args) => retire(args).await,
        AgentCommands::Test(args) => test(args).await,
        AgentCommands::Invoke(args) => invoke(args).await,
        AgentCommands::Config(args) => config(args).await,
        AgentCommands::Card(args) => card(args).await,
        AgentCommands::Versions(args) => versions(args).await,
    }
}

async fn register(args: RegisterArgs) -> anyhow::Result<()> {
    // Determine if we're using TOML config or direct flags
    let use_config =
        args.config.is_some() || (args.name.is_none() && args.model_provider.is_none());

    if use_config {
        let config_path = args.config.as_deref().unwrap_or("agentoven.toml");
        println!(
            "\n  🏺 Reading configuration from {}...",
            config_path.cyan()
        );

        let path = std::path::Path::new(config_path);
        if !path.exists() {
            anyhow::bail!(
                "Configuration file not found: {}. Run `agentoven init` first.",
                config_path
            );
        }

        let config_str = tokio::fs::read_to_string(path).await?;
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

        let framework = parse_framework(framework_str);
        let mode_str = agent_table
            .get("mode")
            .and_then(|v| v.as_str())
            .unwrap_or("managed");
        let mode = if mode_str == "external" {
            AgentMode::External
        } else {
            AgentMode::Managed
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

        let model_provider = agent_table
            .get("model_provider")
            .and_then(|v| v.as_str())
            .unwrap_or("");
        let model_name = agent_table
            .get("model_name")
            .and_then(|v| v.as_str())
            .unwrap_or("");

        let mut builder = Agent::builder(name)
            .version(version)
            .description(description)
            .framework(framework)
            .mode(mode)
            .model_provider(model_provider)
            .model_name(model_name);

        if let Some(bp) = agent_table.get("backup_provider").and_then(|v| v.as_str()) {
            builder = builder.backup_provider(bp);
        }
        if let Some(bm) = agent_table.get("backup_model").and_then(|v| v.as_str()) {
            builder = builder.backup_model(bm);
        }
        if let Some(sp) = agent_table.get("system_prompt").and_then(|v| v.as_str()) {
            builder = builder.system_prompt(sp);
        }
        if let Some(mt) = agent_table.get("max_turns").and_then(|v| v.as_integer()) {
            builder = builder.max_turns(mt as u32);
        }
        if let Some(ep) = agent_table.get("a2a_endpoint").and_then(|v| v.as_str()) {
            builder = builder.a2a_endpoint(ep);
        }

        let agent = ingredients
            .into_iter()
            .fold(builder, |a, i| a.ingredient(i))
            .build();

        println!(
            "  {} Parsed agent: {} ({}, {})",
            "→".dimmed(),
            agent.qualified_name().bold(),
            framework_display(&agent.framework).cyan(),
            agent.mode
        );

        register_agent_with_client(&agent).await
    } else {
        // Direct flag registration
        let name = args
            .name
            .as_deref()
            .ok_or_else(|| anyhow::anyhow!("Agent name required"))?;
        println!("\n  🏺 Registering agent: {}\n", name.bold());

        let framework = parse_framework(args.framework.as_deref().unwrap_or("managed"));
        let mode = if args.mode == "external" {
            AgentMode::External
        } else {
            AgentMode::Managed
        };

        let mut builder = Agent::builder(name)
            .description(args.description.as_deref().unwrap_or(""))
            .framework(framework)
            .mode(mode)
            .model_provider(args.model_provider.as_deref().unwrap_or(""))
            .model_name(args.model_name.as_deref().unwrap_or(""));

        if let Some(ref bp) = args.backup_provider {
            builder = builder.backup_provider(bp.as_str());
        }
        if let Some(ref bm) = args.backup_model {
            builder = builder.backup_model(bm.as_str());
        }
        if let Some(ref sp) = args.system_prompt {
            builder = builder.system_prompt(sp.as_str());
        }
        if let Some(mt) = args.max_turns {
            builder = builder.max_turns(mt);
        }
        if let Some(ref ep) = args.a2a_endpoint {
            builder = builder.a2a_endpoint(ep.as_str());
        }

        for s in &args.skill {
            builder = builder.skill(s.as_str());
        }
        for t in &args.tag {
            builder = builder.tag(t.as_str());
        }

        for g in &args.guardrail {
            let parts: Vec<&str> = g.splitn(2, ':').collect();
            let kind = parts[0].to_string();
            let stage = if parts.len() > 1 {
                parts[1].to_string()
            } else {
                "pre".to_string()
            };
            builder = builder.guardrail(agentoven_core::Guardrail {
                kind,
                stage,
                config: None,
            });
        }

        let agent = builder.build();
        register_agent_with_client(&agent).await
    }
}

async fn register_agent_with_client(agent: &Agent) -> anyhow::Result<()> {
    let client = agentoven_core::AgentOvenClient::from_env()?;
    match client.register(agent).await {
        Ok(registered) => {
            println!("  {} Agent registered successfully!", "✓".green().bold());
            println!("  {} ID: {}", "→".dimmed(), registered.id.cyan());
            println!(
                "  {} Next: {}",
                "→".dimmed(),
                format!("agentoven agent bake {}", agent.name).green()
            );
        }
        Err(e) => {
            println!(
                "  {} Registration failed: {}",
                "⚠".yellow().bold(),
                e.to_string().dimmed()
            );
            println!(
                "  {} Agent validated locally. Start the control plane to register remotely.",
                "→".dimmed(),
            );
        }
    }
    Ok(())
}

fn parse_framework(s: &str) -> AgentFramework {
    match s {
        "langgraph" => AgentFramework::LangGraph,
        "crewai" => AgentFramework::CrewAi,
        "openai" | "openai-sdk" => AgentFramework::OpenAiSdk,
        "autogen" => AgentFramework::AutoGen,
        "agent-framework" => AgentFramework::AgentFramework,
        "managed" => AgentFramework::Managed,
        _ => AgentFramework::Custom,
    }
}

async fn list(_args: ListArgs) -> anyhow::Result<()> {
    println!("\n  📋 Agents in the kitchen:\n");

    let client = agentoven_core::AgentOvenClient::from_env()?;

    match client.list_agents().await {
        Ok(agents) => {
            if agents.is_empty() {
                println!("  (no agents registered yet — use `agentoven agent register`)");
            } else {
                println!(
                    "  {:<20} {:<10} {:<12} {:<10} {:<16} {:<16}",
                    "NAME".bold(),
                    "VERSION".bold(),
                    "STATUS".bold(),
                    "MODE".bold(),
                    "PROVIDER".bold(),
                    "FRAMEWORK".bold()
                );
                println!("  {}", "─".repeat(86).dimmed());
                for agent in &agents {
                    let provider_col = if agent.model_provider.is_empty() {
                        "-".to_string()
                    } else {
                        format!("{}/{}", agent.model_provider, agent.model_name)
                    };
                    println!(
                        "  {:<20} {:<10} {:<12} {:<10} {:<16} {:<16}",
                        agent.name,
                        agent.version,
                        agent.status.to_string(),
                        agent.mode.to_string(),
                        if provider_col.len() > 15 {
                            format!("{}…", &provider_col[..14])
                        } else {
                            provider_col
                        },
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
    println!("\n  🍞 Agent: {}\n", args.name.bold());

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
            println!("  {:<16} {}", "Mode:".bold(), agent.mode);
            println!(
                "  {:<16} {}",
                "Framework:".bold(),
                framework_display(&agent.framework)
            );
            if !agent.description.is_empty() {
                println!("  {:<16} {}", "Description:".bold(), agent.description);
            }
            if !agent.model_provider.is_empty() {
                println!(
                    "  {:<16} {}",
                    "Provider:".bold(),
                    agent.model_provider.cyan()
                );
                println!("  {:<16} {}", "Model:".bold(), agent.model_name.cyan());
            }
            if let Some(ref bp) = agent.backup_provider {
                let bm = agent.backup_model.as_deref().unwrap_or("-");
                println!("  {:<16} {}/{}", "Backup:".bold(), bp, bm);
            }
            if let Some(ref sp) = agent.system_prompt {
                let preview = if sp.len() > 60 {
                    format!("{}...", &sp[..60])
                } else {
                    sp.clone()
                };
                println!("  {:<16} {}", "System Prompt:".bold(), preview.dimmed());
            }
            if let Some(mt) = agent.max_turns {
                println!("  {:<16} {}", "Max Turns:".bold(), mt);
            }
            if !agent.skills.is_empty() {
                println!("  {:<16} {}", "Skills:".bold(), agent.skills.join(", "));
            }
            if !agent.tags.is_empty() {
                println!("  {:<16} {}", "Tags:".bold(), agent.tags.join(", "));
            }
            if let Some(ref ep) = agent.a2a_endpoint {
                println!("  {:<16} {}", "A2A Endpoint:".bold(), ep.cyan());
            }
            if !agent.guardrails.is_empty() {
                println!("\n  {}:", "Guardrails".bold());
                for g in &agent.guardrails {
                    println!("    🛡️ {} (stage: {})", g.kind.cyan(), g.stage);
                }
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
            println!(
                "\n  {:<16} {}",
                "Created:".bold(),
                agent.created_at.format("%Y-%m-%d %H:%M UTC")
            );
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

async fn update(args: UpdateArgs) -> anyhow::Result<()> {
    println!("\n  📝 Updating agent: {}\n", args.name.bold());

    let body = if let Some(ref raw) = args.json {
        serde_json::from_str(raw)?
    } else {
        let mut obj = serde_json::Map::new();
        if let Some(ref v) = args.description {
            obj.insert("description".into(), serde_json::json!(v));
        }
        if let Some(ref v) = args.model_provider {
            obj.insert("model_provider".into(), serde_json::json!(v));
        }
        if let Some(ref v) = args.model_name {
            obj.insert("model_name".into(), serde_json::json!(v));
        }
        if let Some(ref v) = args.system_prompt {
            obj.insert("system_prompt".into(), serde_json::json!(v));
        }
        if let Some(v) = args.max_turns {
            obj.insert("max_turns".into(), serde_json::json!(v));
        }
        if let Some(ref v) = args.backup_provider {
            obj.insert("backup_provider".into(), serde_json::json!(v));
        }
        if let Some(ref v) = args.backup_model {
            obj.insert("backup_model".into(), serde_json::json!(v));
        }
        serde_json::Value::Object(obj)
    };

    let client = agentoven_core::AgentOvenClient::from_env()?;
    match client.update_agent(&args.name, body).await {
        Ok(agent) => {
            println!(
                "  {} Agent '{}' updated (v{}).",
                "✓".green().bold(),
                agent.name,
                agent.version
            );
        }
        Err(e) => {
            println!(
                "  {} Update failed: {}",
                "✗".red().bold(),
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
                "  Delete agent '{}'? This cannot be undone.",
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
    match client.delete_agent(&args.name).await {
        Ok(()) => println!("  {} Agent '{}' deleted.", "✓".green().bold(), args.name),
        Err(e) => println!(
            "  {} Delete failed: {}",
            "✗".red().bold(),
            e.to_string().dimmed()
        ),
    }
    Ok(())
}

async fn bake(args: BakeArgs) -> anyhow::Result<()> {
    println!(
        "\n  🔥 Baking {} to {}...\n",
        args.name.bold(),
        args.environment.cyan()
    );

    let client = agentoven_core::AgentOvenClient::from_env()?;

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

    match client.bake(&agent, &args.environment).await {
        Ok(deployed) => {
            println!("  {} Agent is now baking!", "✓".green().bold());
            println!("  {} Status: {}", "→".dimmed(), deployed.status);
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

async fn recook(args: RecookArgs) -> anyhow::Result<()> {
    println!("\n  🔄 Re-cooking {}...\n", args.name.bold());

    let edits = if let Some(ref raw) = args.json {
        serde_json::from_str(raw)?
    } else {
        let mut obj = serde_json::Map::new();
        if let Some(ref v) = args.model_provider {
            obj.insert("model_provider".into(), serde_json::json!(v));
        }
        if let Some(ref v) = args.model_name {
            obj.insert("model_name".into(), serde_json::json!(v));
        }
        if let Some(ref v) = args.system_prompt {
            obj.insert("system_prompt".into(), serde_json::json!(v));
        }
        serde_json::Value::Object(obj)
    };

    let client = agentoven_core::AgentOvenClient::from_env()?;
    match client.recook_agent(&args.name, edits).await {
        Ok(result) => {
            let status = result["status"].as_str().unwrap_or("ready");
            println!(
                "  {} Agent '{}' re-cooked (status: {}).",
                "✓".green().bold(),
                args.name,
                status
            );
        }
        Err(e) => {
            println!(
                "  {} Re-cook failed: {}",
                "✗".red().bold(),
                e.to_string().dimmed()
            );
        }
    }
    Ok(())
}

async fn cool(args: CoolArgs) -> anyhow::Result<()> {
    println!("\n  ❄️ Cooling down {}...", args.name.bold());

    let client = agentoven_core::AgentOvenClient::from_env()?;
    match client.cool_agent(&args.name).await {
        Ok(result) => {
            let status = result["status"].as_str().unwrap_or("cooled");
            println!(
                "  {} Agent '{}' cooled (status: ⏸️  {}).",
                "✓".green().bold(),
                args.name,
                status
            );
            println!(
                "  {} Re-activate with: {}",
                "→".dimmed(),
                format!("agentoven agent rewarm {}", args.name).green()
            );
        }
        Err(e) => {
            println!(
                "  {} Cool failed: {}",
                "✗".red().bold(),
                e.to_string().dimmed()
            );
        }
    }
    Ok(())
}

async fn rewarm(args: RewarmArgs) -> anyhow::Result<()> {
    println!("\n  ☀️ Rewarming {}...\n", args.name.bold());

    let client = agentoven_core::AgentOvenClient::from_env()?;
    match client.rewarm(&args.name).await {
        Ok(resp) => {
            let status = resp
                .get("status")
                .and_then(|v| v.as_str())
                .unwrap_or("ready");
            println!(
                "  {} Agent '{}' rewarmed (status: 🟢 {}).",
                "✓".green().bold(),
                args.name,
                status
            );
            println!(
                "  {} Test with: {}",
                "→".dimmed(),
                format!("agentoven agent test {}", args.name).green()
            );
        }
        Err(e) => {
            println!(
                "  {} Rewarm failed: {}",
                "✗".red().bold(),
                e.to_string().dimmed()
            );
        }
    }
    Ok(())
}

async fn retire(args: RetireArgs) -> anyhow::Result<()> {
    if !args.force {
        let confirm = dialoguer::Confirm::new()
            .with_prompt(format!(
                "  Are you sure you want to retire '{}'?",
                args.name
            ))
            .default(false)
            .interact()?;
        if !confirm {
            println!("  {} Cancelled.", "→".dimmed());
            return Ok(());
        }
    }

    println!("\n  ⚫ Retiring {}...", args.name.bold());

    let client = agentoven_core::AgentOvenClient::from_env()?;
    match client.retire_agent(&args.name).await {
        Ok(result) => {
            let status = result["status"].as_str().unwrap_or("retired");
            println!(
                "  {} Agent '{}' retired ({}).",
                "✓".green().bold(),
                args.name,
                status
            );
            println!(
                "  {} This action is permanent. The agent will no longer serve requests.",
                "⚠".yellow()
            );
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
    println!("\n  🧪 Testing {} in the playground...\n", args.name.bold());

    let client = agentoven_core::AgentOvenClient::from_env()?;

    // Verify agent exists
    match client.get_agent(&args.name, None).await {
        Ok(agent) => {
            println!(
                "  {} Connected to {} ({}, {})\n",
                "✓".green().bold(),
                agent.qualified_name().cyan(),
                agent.status,
                agent.mode
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

    // One-shot mode: use /test endpoint
    if let Some(msg) = &args.message {
        if !args.interactive {
            println!("  {} {}", "You:".bold(), msg);
            match client.test_agent(&args.name, msg, args.thinking).await {
                Ok(result) => {
                    let reply = result["reply"]
                        .as_str()
                        .or_else(|| result["content"].as_str())
                        .or_else(|| result["message"].as_str());
                    if let Some(text) = reply {
                        println!("\n  🤖 {}:\n", "Agent".green().bold());
                        for line in text.lines() {
                            println!("    {}", line);
                        }
                    }
                    if args.thinking {
                        if let Some(t) = result.get("thinking").and_then(|v| v.as_str()) {
                            println!("\n  💭 {}:", "Thinking".dimmed());
                            for line in t.lines() {
                                println!("    {}", line.dimmed());
                            }
                        }
                    }
                }
                Err(e) => {
                    println!("  {} Error: {}", "✗".red().bold(), e.to_string().dimmed());
                }
            }
            return Ok(());
        }
    }

    // Interactive A2A REPL
    println!(
        "  Type a message and press Enter. Type {} to quit.\n",
        "/exit".dimmed()
    );

    if let Some(msg) = &args.message {
        println!("  {} {}", "You:".bold(), msg);
        let a2a_base = format!("http://localhost:8080/agents/{}/a2a", args.name);
        let a2a_client = a2a_ao::A2AClient::new(&a2a_base);
        match a2a_client.send_message_text(msg).await {
            Ok(task) => {
                println!(
                    "  {} Task created: {} ({})",
                    "Agent:".bold().cyan(),
                    task.id.dimmed(),
                    task.state
                );
                for artifact in &task.artifacts {
                    println!("  {} {}", "→".dimmed(), artifact.text_content());
                }
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

    let a2a_base = format!("http://localhost:8080/agents/{}/a2a", args.name);
    let a2a_client = a2a_ao::A2AClient::new(&a2a_base);
    let mut current_task_id: Option<String> = None;

    while let Ok(input) = dialoguer::Input::<String>::new()
        .with_prompt("  You")
        .interact_text()
    {
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
                        task.artifacts
                            .iter()
                            .map(|a| a.text_content())
                            .collect::<Vec<_>>()
                            .join("\n")
                    } else if !task.messages.is_empty() {
                        task.messages
                            .last()
                            .map(|m| m.text_content())
                            .unwrap_or_default()
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

async fn invoke(args: InvokeArgs) -> anyhow::Result<()> {
    println!(
        "\n  ⚡ Invoking {} (managed agentic loop)...\n",
        args.name.bold()
    );

    let variables = if let Some(ref raw) = args.variables {
        Some(serde_json::from_str(raw)?)
    } else {
        None
    };

    let client = agentoven_core::AgentOvenClient::from_env()?;
    match client
        .invoke_agent(&args.name, &args.message, variables, args.thinking)
        .await
    {
        Ok(result) => {
            // Display reply
            let reply = result["reply"]
                .as_str()
                .or_else(|| result["content"].as_str())
                .or_else(|| result["final_answer"].as_str());
            if let Some(text) = reply {
                println!("  🤖 {}:\n", "Agent".green().bold());
                for line in text.lines() {
                    println!("    {}", line);
                }
            }

            // Display thinking
            if args.thinking {
                if let Some(t) = result.get("thinking").and_then(|v| v.as_str()) {
                    println!("\n  💭 {}:", "Thinking".dimmed());
                    for line in t.lines() {
                        println!("    {}", line.dimmed());
                    }
                }
            }

            // Display execution trace
            if let Some(turns) = result.get("turns").and_then(|v| v.as_array()) {
                println!("\n  {} {} turn(s):", "Execution Trace".bold(), turns.len());
                println!("  {}", "─".repeat(50).dimmed());
                for (i, turn) in turns.iter().enumerate() {
                    let kind = turn["kind"].as_str().unwrap_or("llm");
                    let tokens = turn["tokens"].as_u64().unwrap_or(0);
                    let latency = turn["latency_ms"].as_u64().unwrap_or(0);
                    println!("    {}. {} — {} tokens, {}ms", i + 1, kind, tokens, latency);
                }
            }

            // Display cost
            if let Some(cost) = result.get("cost") {
                println!("\n  💰 Cost: ${:.6}", cost["total"].as_f64().unwrap_or(0.0));
            }
        }
        Err(e) => {
            println!(
                "  {} Invoke failed: {}",
                "✗".red().bold(),
                e.to_string().dimmed()
            );
        }
    }
    Ok(())
}

async fn config(args: ConfigArgs) -> anyhow::Result<()> {
    println!("\n  ⚙️  Resolved config for: {}\n", args.name.bold());

    let client = agentoven_core::AgentOvenClient::from_env()?;
    match client.agent_config(&args.name).await {
        Ok(cfg) => {
            let pretty = serde_json::to_string_pretty(&cfg).unwrap_or_default();
            for line in pretty.lines() {
                println!("  {}", line.dimmed());
            }
        }
        Err(e) => {
            println!(
                "  {} Could not fetch config: {}",
                "⚠".yellow().bold(),
                e.to_string().dimmed()
            );
            println!("  {} Make sure the agent is baked first.", "→".dimmed());
        }
    }
    Ok(())
}

async fn card(args: CardArgs) -> anyhow::Result<()> {
    println!("\n  🪪 A2A Agent Card for: {}\n", args.name.bold());

    let client = agentoven_core::AgentOvenClient::from_env()?;
    match client.agent_card(&args.name).await {
        Ok(card_json) => {
            // Display key fields
            if let Some(name) = card_json["name"].as_str() {
                println!("  {:<16} {}", "Name:".bold(), name);
            }
            if let Some(desc) = card_json["description"].as_str() {
                println!("  {:<16} {}", "Description:".bold(), desc);
            }
            if let Some(url) = card_json["url"].as_str() {
                println!("  {:<16} {}", "URL:".bold(), url.cyan());
            }
            if let Some(ver) = card_json["version"].as_str() {
                println!("  {:<16} {}", "Version:".bold(), ver);
            }
            if let Some(caps) = card_json.get("capabilities") {
                println!("\n  {}:", "Capabilities".bold());
                if caps["streaming"].as_bool().unwrap_or(false) {
                    println!("    {} Streaming", "✓".green());
                }
                if caps["pushNotifications"].as_bool().unwrap_or(false) {
                    println!("    {} Push Notifications", "✓".green());
                }
            }
            if let Some(skills) = card_json["skills"].as_array() {
                if !skills.is_empty() {
                    println!("\n  {}:", "Skills".bold());
                    for skill in skills {
                        let sn = skill["name"].as_str().unwrap_or("-");
                        let sd = skill["description"].as_str().unwrap_or("");
                        println!("    {} {} — {}", "•".dimmed(), sn.cyan(), sd);
                    }
                }
            }

            println!("\n  {}:", "Full Card JSON".bold());
            let pretty = serde_json::to_string_pretty(&card_json).unwrap_or_default();
            for line in pretty.lines() {
                println!("    {}", line.dimmed());
            }
        }
        Err(e) => {
            println!(
                "  {} Could not fetch agent card: {}",
                "⚠".yellow().bold(),
                e.to_string().dimmed()
            );
            println!("  {} Make sure the agent is baked first.", "→".dimmed());
        }
    }
    Ok(())
}

async fn versions(args: VersionsArgs) -> anyhow::Result<()> {
    println!("\n  📜 Version history for: {}\n", args.name.bold());

    let client = agentoven_core::AgentOvenClient::from_env()?;
    match client.agent_versions(&args.name).await {
        Ok(versions) => {
            if versions.is_empty() {
                println!("  (no version history)");
            } else {
                println!(
                    "  {:<10} {:<14} {:<20} {:<12}",
                    "VERSION".bold(),
                    "STATUS".bold(),
                    "CREATED".bold(),
                    "CHANGES".bold()
                );
                println!("  {}", "─".repeat(58).dimmed());
                for v in &versions {
                    let ver = v["version"].as_str().unwrap_or("-");
                    let status = v["status"].as_str().unwrap_or("-");
                    let created = v["created_at"].as_str().unwrap_or("-");
                    let created_short = if created.len() > 16 {
                        &created[..16]
                    } else {
                        created
                    };
                    let changes = v["change_summary"].as_str().unwrap_or("-");
                    println!(
                        "  {:<10} {:<14} {:<20} {}",
                        ver, status, created_short, changes
                    );
                }
                println!("\n  {} {} version(s)", "→".dimmed(), versions.len());
            }
        }
        Err(e) => {
            println!("  {} Failed: {}", "✗".red().bold(), e.to_string().dimmed());
        }
    }
    Ok(())
}
