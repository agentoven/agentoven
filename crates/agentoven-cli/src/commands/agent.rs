//! `agentoven agent` — manage agents in the oven.

use clap::{Args, Subcommand};
use colored::Colorize;

use agentoven_core::agent::{Agent, AgentFramework, AgentMode};
use agentoven_core::ingredient::Ingredient;
use agentoven_core::refcheck::Options;
use agentoven_core::AgentOvenClient;

use super::refs;

fn framework_display(f: &AgentFramework) -> &'static str {
    match f {
        AgentFramework::Langchain => "langchain",
        AgentFramework::Deepagents => "deepagents",
        AgentFramework::Crewai => "crewai",
        AgentFramework::Openai => "openai",
        AgentFramework::Autogen => "autogen",
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

    /// Check a stored agent's configuration against the kitchen: every provider, model, tool, skill, prompt and
    /// subagent it names must exist. Prints each problem with the nearest valid values; exits 1 when any
    /// would refuse the agent at its next bake. Asks the control plane (read only; nothing changes).
    Validate(ValidateArgs),

    /// Show the A2A Agent Card (discovery metadata).
    Card(CardArgs),

    /// List the versions an agent has been baked at, or show one.
    ///
    /// A version moves only on a bake whose configuration differs from the last bake's. The first bake
    /// keeps the registered version (0.1.0), a bake that changes nothing keeps its version, and a
    /// changed one takes the next minor version and keeps the old one. Editing an agent (`register`,
    /// `update`) does not bump it.
    Versions(VersionsArgs),
}

#[derive(Args)]
pub struct RegisterArgs {
    /// Agent name (optional if using --config or --from).
    pub name: Option<String>,

    /// Path to agentoven.toml.
    #[arg(long, short)]
    pub config: Option<String>,

    /// Path to agent definition file (YAML/JSON/TOML).
    #[arg(long, short = 'f')]
    pub from: Option<String>,

    // ── Direct flags (used when not using --config) ──
    /// Agent description.
    #[arg(long)]
    pub description: Option<String>,

    /// Framework (langgraph, deepagents, crewai, openai-sdk, autogen, managed, custom).
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

    /// Provider that serves the compaction (summarizer) model; with --summary-model.
    #[arg(long)]
    pub summary_provider: Option<String>,

    /// Model that summarizes the agent's history when it outgrows its context (compaction).
    #[arg(long)]
    pub summary_model: Option<String>,

    /// Check the configuration against the kitchen before sending it (the control plane checks it anyway).
    #[arg(long)]
    pub validate: bool,
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

    /// New provider for the compaction (summarizer) model ("" clears it).
    #[arg(long)]
    pub summary_provider: Option<String>,

    /// New compaction (summarizer) model ("" uses the primary model again).
    #[arg(long)]
    pub summary_model: Option<String>,

    /// Raw JSON update body (overrides other flags).
    #[arg(long)]
    pub json: Option<String>,

    /// Check the update against the kitchen before sending it (the control plane checks it anyway).
    #[arg(long)]
    pub validate: bool,
}

#[derive(Args)]
pub struct ValidateArgs {
    /// Agent name.
    pub name: String,
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
// `--version` below is the agent's version, not the program's: without this clap's debug build refuses to
// parse the command at all (two arguments named "version").
#[command(disable_version_flag = true)]
pub struct BakeArgs {
    /// Agent name.
    pub name: String,

    /// Target environment.
    #[arg(long, short, default_value = "production")]
    pub environment: String,

    /// Bake this stored version of the agent (defaults to the agent as it is now). The agent's version
    /// itself moves only on a bake whose configuration differs from the last bake's: the first bake keeps
    /// the registered version (0.1.0), edits do not bump it.
    #[arg(long, short)]
    pub version: Option<String>,

    /// Check the agent against the kitchen before baking (the control plane checks it anyway). It judges the
    /// agent as it is now, so it cannot be combined with --version.
    #[arg(long, conflicts_with = "version")]
    pub validate: bool,
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

    /// Check the stored agent with these edits against the kitchen before sending (the control plane checks it anyway).
    #[arg(long)]
    pub validate: bool,
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
    /// Show this one version as it was configured when it was baked (0.1.0 is the first bake).
    #[arg(value_name = "VERSION")]
    pub number: Option<String>,
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
        AgentCommands::Validate(args) => validate(args).await,
        AgentCommands::Card(args) => card(args).await,
        AgentCommands::Versions(args) => versions(args).await,
    }
}

async fn register(args: RegisterArgs) -> anyhow::Result<()> {
    // If --from is provided, parse the file and register from it
    if let Some(ref from_path) = args.from {
        return register_from_file(from_path, args.validate).await;
    }

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
        if let Some(sp) = agent_table.get("summary_provider").and_then(|v| v.as_str()) {
            builder = builder.summary_provider(sp);
        }
        if let Some(sm) = agent_table.get("summary_model").and_then(|v| v.as_str()) {
            builder = builder.summary_model_name(sm);
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

        register_agent_with_client(&agent, args.validate).await
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
        if let Some(ref sp) = args.summary_provider {
            builder = builder.summary_provider(sp.as_str());
        }
        if let Some(ref sm) = args.summary_model {
            builder = builder.summary_model_name(sm.as_str());
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
        register_agent_with_client(&agent, args.validate).await
    }
}

/// Register an agent from a YAML/JSON/TOML definition file.
async fn register_from_file(path: &str, validate: bool) -> anyhow::Result<()> {
    println!("\n  🏺 Registering agent from {}...\n", path.cyan());

    let content = tokio::fs::read_to_string(path).await?;

    let parsed: serde_json::Value = if path.ends_with(".yaml") || path.ends_with(".yml") {
        serde_yaml::from_str(&content)?
    } else if path.ends_with(".toml") {
        let toml_val: toml::Value = content.parse()?;
        serde_json::to_value(toml_val)?
    } else {
        serde_json::from_str(&content)?
    };

    let name = parsed["name"]
        .as_str()
        .ok_or_else(|| anyhow::anyhow!("Missing 'name' field in {}", path))?;
    let version = parsed["version"].as_str().unwrap_or("0.1.0");
    let description = parsed["description"].as_str().unwrap_or("");
    let framework_str = parsed["framework"].as_str().unwrap_or("managed");
    let mode_str = parsed["mode"].as_str().unwrap_or("managed");

    let framework = parse_framework(framework_str);
    let mode = if mode_str == "external" {
        AgentMode::External
    } else {
        AgentMode::Managed
    };

    let mut builder = Agent::builder(name)
        .version(version)
        .description(description)
        .framework(framework)
        .mode(mode)
        .model_provider(parsed["model_provider"].as_str().unwrap_or(""))
        .model_name(parsed["model_name"].as_str().unwrap_or(""));

    if let Some(bp) = parsed["backup_provider"].as_str() {
        builder = builder.backup_provider(bp);
    }
    if let Some(bm) = parsed["backup_model"].as_str() {
        builder = builder.backup_model(bm);
    }
    if let Some(sp) = parsed["summary_provider"].as_str() {
        builder = builder.summary_provider(sp);
    }
    if let Some(sm) = parsed["summary_model"].as_str() {
        builder = builder.summary_model_name(sm);
    }
    if let Some(sp) = parsed["system_prompt"].as_str() {
        builder = builder.system_prompt(sp);
    }
    if let Some(mt) = parsed["max_turns"].as_u64() {
        builder = builder.max_turns(mt as u32);
    }
    if let Some(ep) = parsed["a2a_endpoint"].as_str() {
        builder = builder.a2a_endpoint(ep);
    }

    // Parse ingredients if present
    let mut ingredients = Vec::new();
    if let Some(ing) = parsed.get("ingredients") {
        if let Some(models) = ing.get("models").and_then(|v| v.as_array()) {
            for m in models {
                let ing_name = m["name"].as_str().unwrap_or("unknown");
                let mut ib = Ingredient::model(ing_name);
                if let Some(p) = m["provider"].as_str() {
                    ib = ib.provider(p);
                }
                if let Some(r) = m["role"].as_str() {
                    ib = ib.role(r);
                }
                ingredients.push(ib.build());
            }
        }
        if let Some(tools) = ing.get("tools").and_then(|v| v.as_array()) {
            for t in tools {
                let ing_name = t["name"].as_str().unwrap_or("unknown");
                let mut ib = Ingredient::tool(ing_name);
                if let Some(p) = t["protocol"].as_str() {
                    ib = ib.provider(p);
                }
                ingredients.push(ib.build());
            }
        }
    }

    let agent = ingredients
        .into_iter()
        .fold(builder, |a, i| a.ingredient(i))
        .build();

    println!(
        "  {} Parsed: {} ({}, {})",
        "→".dimmed(),
        agent.qualified_name().bold(),
        framework_display(&agent.framework).cyan(),
        agent.mode
    );

    register_agent_with_client(&agent, validate).await
}

async fn register_agent_with_client(agent: &Agent, validate: bool) -> anyhow::Result<()> {
    let client = AgentOvenClient::from_env()?;
    register_with(&client, agent, validate).await
}

async fn register_with(
    client: &AgentOvenClient,
    agent: &Agent,
    validate: bool,
) -> anyhow::Result<()> {
    if validate {
        refs::gate(
            client
                .validate_agent(&serde_json::to_value(agent)?, &Options::write())
                .await?,
        )?;
    }
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
            if let Some(refused) = refs::refused(&e) {
                return Err(refused);
            }
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
        "langgraph" | "langchain" => AgentFramework::Langchain,
        "deepagents" | "deep-agents" | "deep_agents" => AgentFramework::Deepagents,
        "crewai" => AgentFramework::Crewai,
        "openai" | "openai-sdk" => AgentFramework::Openai,
        "autogen" => AgentFramework::Autogen,
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
        if let Some(ref v) = args.summary_provider {
            obj.insert("summary_provider".into(), serde_json::json!(v));
        }
        if let Some(ref v) = args.summary_model {
            obj.insert("summary_model".into(), serde_json::json!(v));
        }
        serde_json::Value::Object(obj)
    };

    let client = AgentOvenClient::from_env()?;
    update_with(&client, &args.name, body, args.validate).await
}

async fn update_with(
    client: &AgentOvenClient,
    name: &str,
    body: serde_json::Value,
    validate: bool,
) -> anyhow::Result<()> {
    if validate {
        // The edit is all the client has: it is judged on its own, as a write.
        refs::gate(client.validate_agent(&body, &Options::write()).await?)?;
    }
    match client.update_agent(name, body).await {
        Ok(agent) => {
            println!(
                "  {} Agent '{}' updated (v{}).",
                "✓".green().bold(),
                agent.name,
                agent.version
            );
        }
        Err(e) => {
            if let Some(refused) = refs::refused(&e) {
                return Err(refused);
            }
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

    let client = AgentOvenClient::from_env()?;
    bake_with(&client, &args).await
}

async fn bake_with(client: &AgentOvenClient, args: &BakeArgs) -> anyhow::Result<()> {
    if args.validate {
        refs::gate(client.validate_stored_agent(&args.name, None).await?)?;
    }
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
            if let Some(refused) = refs::refused(&e) {
                return Err(refused);
            }
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

    let client = AgentOvenClient::from_env()?;
    if args.validate {
        refs::gate(
            client
                .validate_stored_agent(&args.name, Some(&edits))
                .await?,
        )?;
    }
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
            if let Some(refused) = refs::refused(&e) {
                return Err(refused);
            }
            println!(
                "  {} Re-cook failed: {}",
                "✗".red().bold(),
                e.to_string().dimmed()
            );
        }
    }
    Ok(())
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

/// The report of `agent validate` and its exit code; kept free of printing and `process::exit` so it can be
/// tested against a fake control plane.
async fn validate_with(client: &AgentOvenClient, name: &str) -> anyhow::Result<(String, i32)> {
    let verdict = client.agent_validate(name).await?;
    Ok(refs::render_validation("agent", &verdict))
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
        let a2a_client = a2a_client_for(&args.name);
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

    let a2a_client = a2a_client_for(&args.name);
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
    let client = agentoven_core::AgentOvenClient::from_env()?;
    if let Some(version) = &args.number {
        let v = client.agent_version(&args.name, version).await?;
        println!("{}", serde_json::to_string_pretty(&v)?);
        return Ok(());
    }
    println!("\n  📜 Version history for: {}\n", args.name.bold());
    println!(
        "  {}",
        "A version moves only on a bake with a changed configuration; the first bake is 0.1.0; edits do not bump it.".dimmed()
    );
    println!();

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

/// An A2A client for one agent, pointed at the configured control plane and carrying the
/// configured credential. Running an agent over A2A needs an identity, like `invoke` does.
fn a2a_client_for(agent: &str) -> a2a_ao::A2AClient {
    let cfg = agentoven_core::AgentOvenConfig::load();
    let base = format!("{}/agents/{}/a2a", cfg.url.trim_end_matches('/'), agent);
    let client = a2a_ao::A2AClient::new(&base);
    match cfg.auth_credential() {
        Some(token) => client.with_auth(token),
        None => client,
    }
}

#[cfg(test)]
mod tests {
    use super::*;
    use crate::commands::{Cli, Commands};
    use agentoven_core::testing::{fake_control_plane, json as reply, Log, Reply, Routes};
    use clap::Parser;
    use serde_json::{json, Value};

    fn parse(args: &[&str]) -> AgentCommands {
        let mut v = vec!["agentoven", "agent"];
        v.extend_from_slice(args);
        match Cli::try_parse_from(v).expect("parse").command {
            Commands::Agent(c) => *c,
            _ => panic!("not an agent command"),
        }
    }

    async fn serve(routes: Vec<(&str, Reply)>) -> (AgentOvenClient, Log) {
        colored::control::set_override(false);
        let mut r = Routes::new();
        for (k, v) in routes {
            r.insert(k.to_string(), v);
        }
        let (base, log) = fake_control_plane(r).await;
        (
            AgentOvenClient::new(&base).unwrap().with_kitchen("acme"),
            log,
        )
    }

    /// The lists `--validate` reads: one provider, no tools.
    fn lists() -> Vec<(&'static str, Reply)> {
        vec![
            (
                "GET /api/v1/models/providers",
                reply(
                    200,
                    json!([{"name": "anthropic", "kind": "anthropic", "models": ["claude-haiku-4-5"]}]),
                ),
            ),
            ("GET /api/v1/prompts", reply(200, json!([]))),
            ("GET /api/v1/tools", reply(200, json!([]))),
            ("GET /api/v1/skills", reply(200, json!([]))),
            ("GET /api/v1/agents", reply(200, json!([]))),
            ("GET /api/v1/recipes", reply(200, json!([]))),
            ("GET /api/v1/channels", reply(200, json!([]))),
            ("GET /api/v1/environments", reply(200, json!([]))),
            ("GET /api/v1/credentials", reply(200, json!([]))),
            ("GET /api/v1/scenarios", reply(404, json!({}))),
        ]
    }

    fn writes(log: &Log) -> Vec<String> {
        log.calls()
            .into_iter()
            .filter(|c| !c.starts_with("GET "))
            .collect()
    }

    const PROBLEM: &str = r#"{"path": "summary_model", "code": "not_found", "severity": "error", "kind": "model", "value": "claude-hiku-4-5",
        "within": "anthropic", "message": "summary_model: \"claude-hiku-4-5\" is not a model of provider \"anthropic\"", "suggestions": ["claude-haiku-4-5"]}"#;

    fn problem() -> Value {
        serde_json::from_str(PROBLEM).unwrap()
    }

    #[test]
    fn the_flags_for_the_summary_model_and_validation_parse() {
        match parse(&[
            "register",
            "bot",
            "--summary-provider",
            "anthropic",
            "--summary-model",
            "claude-haiku-4-5",
            "--validate",
        ]) {
            AgentCommands::Register(a) => {
                assert_eq!(a.summary_provider.as_deref(), Some("anthropic"));
                assert_eq!(a.summary_model.as_deref(), Some("claude-haiku-4-5"));
                assert!(a.validate);
            }
            _ => panic!("not register"),
        }
        match parse(&["update", "bot", "--summary-model", ""]) {
            AgentCommands::Update(a) => {
                assert_eq!(
                    a.summary_model.as_deref(),
                    Some(""),
                    "an empty summary model clears it"
                );
                assert!(!a.validate, "off unless asked for");
            }
            _ => panic!("not update"),
        }
        assert!(
            matches!(parse(&["validate", "bot"]), AgentCommands::Validate(a) if a.name == "bot")
        );
        assert!(
            matches!(parse(&["bake", "bot", "--validate"]), AgentCommands::Bake(a) if a.validate)
        );
        assert!(
            matches!(parse(&["recook", "bot", "--validate"]), AgentCommands::Recook(a) if a.validate)
        );
    }

    #[test]
    fn validating_a_bake_of_an_older_version_is_refused_as_ambiguous() {
        let err = Cli::try_parse_from([
            "agentoven",
            "agent",
            "bake",
            "bot",
            "--validate",
            "--version",
            "0.1.0",
        ])
        .err()
        .expect("a conflict");
        assert!(err.to_string().contains("cannot be used with"), "{err}");
    }

    #[tokio::test]
    async fn agent_validate_prints_each_problem_with_what_was_meant_and_exits_one() {
        let reply_body =
            json!({"agent": "bot", "valid": false, "problems": [problem()], "warnings": []});
        let (client, log) = serve(vec![(
            "GET /api/v1/agents/bot/validate",
            reply(200, reply_body),
        )])
        .await;
        let (text, code) = validate_with(&client, "bot").await.unwrap();
        assert_eq!(code, 1);
        assert!(
            text.contains("summary_model = \"claude-hiku-4-5\""),
            "{text}"
        );
        assert!(text.contains("did you mean: claude-haiku-4-5"), "{text}");
        assert_eq!(
            log.calls(),
            vec!["GET /api/v1/agents/bot/validate"],
            "read only"
        );
    }

    #[tokio::test]
    async fn agent_validate_of_a_valid_agent_exits_zero_and_of_a_missing_one_fails() {
        let ok = json!({"agent": "bot", "valid": true, "problems": [], "warnings": []});
        let (client, _) = serve(vec![("GET /api/v1/agents/bot/validate", reply(200, ok))]).await;
        let (text, code) = validate_with(&client, "bot").await.unwrap();
        assert_eq!(code, 0);
        assert!(text.contains("agent bot is valid"), "{text}");
        assert!(
            validate_with(&client, "nobody").await.is_err(),
            "a 404 is an error, not a verdict"
        );
    }

    #[tokio::test]
    async fn update_with_validate_stops_on_a_bad_name_and_sends_nothing() {
        let mut routes = lists();
        routes.push(("PUT /api/v1/agents/bot", reply(200, json!({}))));
        let (client, log) = serve(routes).await;
        let err = update_with(
            &client,
            "bot",
            json!({"summary_provider": "anthropic", "summary_model": "claude-hiku-4-5"}),
            true,
        )
        .await
        .unwrap_err();
        assert!(err.to_string().contains("1 problem"), "{err}");
        assert!(writes(&log).is_empty(), "{:?}", log.calls());
    }

    #[tokio::test]
    async fn update_with_sends_the_summary_model_when_it_is_valid() {
        let stored = serde_json::to_value(Agent::builder("bot").version("1.0.0").build()).unwrap();
        let mut routes = lists();
        routes.push(("PUT /api/v1/agents/bot", reply(200, stored)));
        let (client, log) = serve(routes).await;
        update_with(
            &client,
            "bot",
            json!({"summary_provider": "anthropic", "summary_model": "claude-haiku-4-5"}),
            true,
        )
        .await
        .unwrap();
        let put = log.all().into_iter().find(|s| s.method == "PUT").unwrap();
        assert_eq!(put.body["summary_model"], "claude-haiku-4-5");
    }

    #[tokio::test]
    async fn a_refusal_from_the_server_ends_the_command_with_a_failure_and_lists_the_problems() {
        let body = json!({"error": "summary_model: bad", "code": "invalid_configuration", "problems": [problem()], "warnings": []});
        let (client, log) = serve(vec![("PUT /api/v1/agents/bot", reply(422, body))]).await;
        let err = update_with(
            &client,
            "bot",
            json!({"summary_model": "claude-hiku-4-5"}),
            false,
        )
        .await
        .unwrap_err();
        assert!(
            err.to_string()
                .contains("names things this kitchen does not have"),
            "{err}"
        );
        assert_eq!(
            writes(&log),
            vec!["PUT /api/v1/agents/bot"],
            "validation was off, so the server judged it"
        );
    }

    #[tokio::test]
    async fn any_other_failure_still_reports_and_succeeds_as_before() {
        let (client, _) = serve(vec![(
            "PUT /api/v1/agents/bot",
            reply(500, json!({"error": "boom"})),
        )])
        .await;
        update_with(&client, "bot", json!({"description": "x"}), false)
            .await
            .unwrap();
    }

    #[tokio::test]
    async fn register_sends_the_summary_model_and_checks_it_with_validate() {
        let stored = serde_json::to_value(Agent::builder("bot").build()).unwrap();
        let mut routes = lists();
        routes.push(("GET /api/v1/agents/bot", reply(404, json!({}))));
        routes.push(("POST /api/v1/agents", reply(201, stored)));
        let (client, log) = serve(routes).await;
        let bad = Agent::builder("bot")
            .summary_model("anthropic", "claude-hiku-4-5")
            .build();
        assert!(register_with(&client, &bad, true).await.is_err());
        assert!(writes(&log).is_empty());
        let good = Agent::builder("bot")
            .summary_model("anthropic", "claude-haiku-4-5")
            .build();
        register_with(&client, &good, true).await.unwrap();
        let post = log.all().into_iter().find(|s| s.method == "POST").unwrap();
        assert_eq!(post.body["summary_provider"], "anthropic");
    }

    #[tokio::test]
    async fn bake_with_validate_reads_the_stored_agent_and_refuses_before_baking() {
        // A managed agent with no model anywhere cannot run: refused at a bake, only a warning on a write.
        let mut routes = lists();
        routes.push((
            "GET /api/v1/agents/bot",
            reply(
                200,
                serde_json::to_value(Agent::builder("bot").build()).unwrap(),
            ),
        ));
        routes.push(("POST /api/v1/agents/bot/bake", reply(200, json!({}))));
        let (client, log) = serve(routes).await;
        let args = BakeArgs {
            name: "bot".into(),
            environment: "prod".into(),
            version: None,
            validate: true,
        };
        let err = bake_with(&client, &args).await.unwrap_err();
        assert!(err.to_string().contains("1 problem"), "{err}");
        assert!(writes(&log).is_empty(), "{:?}", log.calls());
    }

    #[tokio::test]
    async fn bake_with_validate_goes_on_when_the_stored_agent_is_sound() {
        let sound = Agent::builder("bot")
            .model_provider("anthropic")
            .model_name("claude-haiku-4-5")
            .build();
        let mut routes = lists();
        routes.push((
            "GET /api/v1/agents/bot",
            reply(200, serde_json::to_value(&sound).unwrap()),
        ));
        routes.push((
            "POST /api/v1/agents/bot/bake",
            reply(200, serde_json::to_value(&sound).unwrap()),
        ));
        let (client, log) = serve(routes).await;
        let args = BakeArgs {
            name: "bot".into(),
            environment: "prod".into(),
            version: None,
            validate: true,
        };
        bake_with(&client, &args).await.unwrap();
        assert_eq!(writes(&log), vec!["POST /api/v1/agents/bot/bake"]);
    }
}
