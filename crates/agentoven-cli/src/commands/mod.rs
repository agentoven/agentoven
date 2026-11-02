//! CLI command definitions and dispatch.

pub mod agent;
pub mod apply;
pub mod config_cmd;
pub mod dashboard;
pub mod environment;
pub mod init;
pub mod kitchen;
pub mod local;
pub mod login;
pub mod pro_gate;
pub mod prompt;
pub mod provider;
pub mod rag;
pub mod recipe;
pub mod server;
pub mod service_account;
pub mod session;
pub mod skills;
pub mod connector;
pub mod dataset;
pub mod key;
pub mod legal_hold;
pub mod secret;
pub mod budgets;
pub mod cache;
pub mod signing;
pub mod limits;
pub mod eval;
pub mod judge;
pub mod scenario;
pub mod test_suite;
pub mod world_schema;
pub mod task;
pub mod tool;
pub mod trace;
pub mod use_cmd;

use clap::{Parser, Subcommand};

const BANNER: &str = r#"
   🏺 AgentOven
   Bake production-ready AI agents.
"#;

/// AgentOven CLI — the enterprise agent control plane.
#[derive(Parser)]
#[command(
    name = "agentoven",
    version,
    about = "🏺 AgentOven — Bake production-ready AI agents",
    long_about = BANNER,
    propagate_version = true
)]
pub struct Cli {
    #[command(subcommand)]
    pub command: Commands,

    /// Control plane URL (overrides config and AGENTOVEN_URL).
    #[arg(long, global = true, env = "AGENTOVEN_URL")]
    pub url: Option<String>,

    /// API key (overrides config and AGENTOVEN_API_KEY).
    #[arg(long, global = true, env = "AGENTOVEN_API_KEY")]
    pub api_key: Option<String>,

    /// Kitchen (workspace) to use.
    #[arg(long, short = 'k', global = true, env = "AGENTOVEN_KITCHEN")]
    pub kitchen: Option<String>,

    /// Output format.
    #[arg(long, global = true, default_value = "text")]
    pub output: OutputFormat,
}

#[derive(Subcommand)]
pub enum Commands {
    /// 🏺 Initialize a new AgentOven project in the current directory.
    Init(init::InitArgs),

    /// 📦 Apply resources from a YAML/JSON/TOML manifest (declarative mode).
    Apply(apply::ApplyArgs),

    /// 🍞 Manage agents in the oven (register, list, bake, invoke, retire).
    #[command(subcommand)]
    Agent(Box<agent::AgentCommands>),

    /// 🔌 Manage model providers (OpenAI, Anthropic, Ollama, etc.).
    #[command(subcommand)]
    Provider(provider::ProviderCommands),

    /// 🧩 Install and review Agent Skills (SKILL.md bundles).
    #[command(subcommand)]
    Skills(skills::SkillsCommands),

    /// 🛠️  Manage MCP tools.
    #[command(subcommand)]
    Tool(tool::ToolCommands),

    /// 📝 Manage versioned prompt templates.
    #[command(subcommand)]
    Prompt(prompt::PromptCommands),

    /// 📖 Manage recipes (multi-agent workflows).
    #[command(subcommand)]
    Recipe(recipe::RecipeCommands),

    /// 💬 Manage multi-turn chat sessions.
    #[command(subcommand)]
    Session(session::SessionCommands),

    /// 🏠 Manage kitchens (workspaces/tenants).
    #[command(subcommand)]
    Kitchen(kitchen::KitchenCommands),

    /// 🔍 Inspect traces and observability data.
    #[command(subcommand)]
    Trace(trace::TraceCommands),

    /// 🔎 RAG pipeline operations (query, ingest).
    #[command(subcommand)]
    Rag(rag::RagCommands),

    /// 🖥️  Run a local AgentOven server (downloads on first run).
    #[command(subcommand)]
    Local(local::LocalCommands),

    /// 🚀 Start the control plane server in the foreground.
    Server(server::ServerArgs),

    /// 🌐 Start the control plane and open the dashboard UI.
    Dashboard(dashboard::DashboardArgs),

    /// 🔑 Authenticate with the AgentOven control plane (Pro).
    Login(login::LoginArgs),

    /// ⚙️  Manage CLI configuration (URL, API key, kitchen).
    #[command(subcommand)]
    Config(config_cmd::ConfigCommands),

    /// 🏠 Switch the active kitchen (workspace).
    Use(use_cmd::UseArgs),

    /// 🌍 Manage deployment environments (Pro).
    #[command(subcommand)]
    Environment(environment::EnvironmentCommands),

    /// 🧪 Manage test suites (Pro).
    #[command(subcommand)]
    TestSuite(test_suite::TestSuiteCommands),

    /// 🌍 Manage scenario environments (Pro).
    #[command(subcommand)]
    Scenario(scenario::ScenarioCommands),

    /// 📐 Manage world schemas (Pro).
    #[command(subcommand)]
    Schema(world_schema::SchemaCommands),

    /// 🔌 Manage data connectors (Pro).
    #[command(subcommand)]
    Connector(connector::ConnectorCommands),

    /// ⚖️  Manage LLM-as-judge verifiers and their statistics (Pro).
    #[command(subcommand)]
    Judge(judge::JudgeCommands),

    /// 🧪 Set a kitchen up to evaluate over tools and A2A (Pro).
    #[command(subcommand)]
    Eval(eval::EvalCommands),

    /// 🤖 Manage service accounts (Pro).
    #[command(subcommand)]
    ServiceAccount(service_account::ServiceAccountCommands),

    /// 🗂  Manage governed datasets: cases, versions, retention, export, purge (Pro).
    #[command(subcommand)]
    Dataset(dataset::DatasetCommands),

    /// ✍️  Signed decisions: list, verify, sign, and who may approve what (Pro).
    #[command(subcommand)]
    Signature(signing::SignatureCommands),

    /// ⚖️  Place or lift a legal hold on a dataset or the audit trail (Pro).
    #[command(subcommand)]
    LegalHold(legal_hold::LegalHoldCommands),

    /// 🔑 Manage scoped API keys and their owners (Pro).
    #[command(subcommand)]
    Key(key::KeyCommands),

    /// 🗝  Secrets and where they live: create, link, rotate, bind to skills, migrate between providers (Pro).
    #[command(subcommand)]
    Secret(secret::SecretCommands),

    /// ⏳ Background tasks: start, follow, extend, resume and cancel long-running agent work.
    #[command(subcommand)]
    /// 💰 Period budgets for the kitchen, an agent, a provider or an API key, in dollars or tokens (Pro).
    #[command(subcommand)]
    Budgets(budgets::BudgetsCommands),

    /// 🚦 Rate limits: requests, tokens and invocations per minute (Pro).
    #[command(subcommand)]
    Limits(limits::LimitsCommands),

    /// 🧊 The response cache: policy per kitchen or agent, and purge (Pro).
    #[command(subcommand)]
    Cache(cache::CacheCommands),

    Task(task::TaskCommands),

    /// 📊 Show control plane status and health.
    Status,
}

#[derive(Clone, clap::ValueEnum)]
pub enum OutputFormat {
    Text,
    Json,
    Table,
}

/// Execute the CLI command.
pub async fn execute(cli: Cli) -> anyhow::Result<()> {
    let Cli {
        command,
        url,
        api_key,
        kitchen,
        output,
    } = cli;
    match command {
        Commands::Init(args) => init::execute(args).await,
        Commands::Apply(args) => apply::execute(args).await,
        Commands::Agent(cmd) => agent::execute(*cmd).await,
        Commands::Provider(cmd) => provider::execute(cmd).await,
        Commands::Skills(cmd) => {
            skills::execute(
                cmd,
                skills::Globals {
                    url,
                    api_key,
                    kitchen,
                    output,
                },
            )
            .await
        }
        Commands::Tool(cmd) => tool::execute(cmd).await,
        Commands::Prompt(cmd) => prompt::execute(cmd).await,
        Commands::Recipe(cmd) => recipe::execute(cmd).await,
        Commands::Session(cmd) => session::execute(cmd).await,
        Commands::Kitchen(cmd) => kitchen::execute(cmd).await,
        Commands::Trace(cmd) => trace::execute(cmd).await,
        Commands::Rag(cmd) => rag::execute(cmd).await,
        Commands::Local(cmd) => local::execute(cmd).await,
        Commands::Server(args) => server::execute(args).await,
        Commands::Dashboard(args) => dashboard::execute(args).await,
        Commands::Login(args) => login::execute(args).await,
        Commands::Config(cmd) => config_cmd::execute(cmd).await,
        Commands::Use(args) => use_cmd::execute(args).await,
        Commands::Environment(cmd) => environment::execute(cmd).await,
        Commands::TestSuite(cmd) => test_suite::execute(cmd).await,
        Commands::Scenario(cmd) => scenario::execute(cmd).await,
        Commands::Schema(cmd) => world_schema::execute(cmd).await,
        Commands::Connector(cmd) => connector::execute(cmd).await,
        Commands::Judge(cmd) => judge::execute(cmd).await,
        Commands::Eval(cmd) => eval::execute(cmd).await,
        Commands::ServiceAccount(cmd) => service_account::execute(cmd).await,
        Commands::Dataset(cmd) => dataset::execute(cmd, signing::client_with(url, api_key, kitchen)?).await,
        Commands::Signature(cmd) => signing::execute(cmd, signing::client_with(url, api_key, kitchen)?).await,
        Commands::LegalHold(cmd) => legal_hold::execute(cmd, signing::client_with(url, api_key, kitchen)?).await,
        Commands::Key(cmd) => key::execute(cmd, signing::client_with(url, api_key, kitchen)?).await,
        Commands::Secret(cmd) => secret::execute(cmd, signing::client_with(url, api_key, kitchen)?).await,
        Commands::Task(cmd) => task::execute(cmd, task::Globals { url, api_key, kitchen }).await,
        Commands::Status => status().await,
    }
}

async fn status() -> anyhow::Result<()> {
    println!("{BANNER}");

    let client = agentoven_core::AgentOvenClient::from_env()?;

        Commands::Budgets(cmd) => {
            budgets::execute(cmd, signing::client_with(url, api_key, kitchen)?, matches!(output, OutputFormat::Json)).await
        }
        Commands::Limits(cmd) => {
            limits::execute(cmd, signing::client_with(url, api_key, kitchen)?, matches!(output, OutputFormat::Json)).await
        }
        Commands::Cache(cmd) => {
            cache::execute(cmd, signing::client_with(url, api_key, kitchen)?, matches!(output, OutputFormat::Json)).await
        }
    // Try to get server info
    print!("  Control plane: checking...");
    match client.server_info().await {
        Ok(info) => {
            let edition = info["edition"].as_str().unwrap_or("community");
            let version = info["version"].as_str().unwrap_or("unknown");
            println!("\r  Control plane: 🟢 connected       ");
            println!("  Server:        {} v{}", edition, version);

            // Also check agents
            if let Ok(agents) = client.list_agents().await {
                println!("  Agents:        {} registered", agents.len());
            }
        }
        Err(_) => {
            // Fall back to listing agents
            match client.list_agents().await {
                Ok(agents) => {
                    println!("\r  Control plane: 🟢 connected    ");
                    println!("  Agents:        {} registered", agents.len());
                }
                Err(_) => {
                    println!("\r  Control plane: 🔴 unreachable   ");
                    println!("  Tip:           Run `agentoven local up` or set AGENTOVEN_URL");
                }
            }
        }
    }

    let config = agentoven_core::AgentOvenConfig::load();
    println!("  URL:           {}", config.url);
    println!(
        "  Kitchen:       {}",
        config.kitchen.as_deref().unwrap_or("default")
    );
    println!("  CLI version:   {}", env!("CARGO_PKG_VERSION"));
    Ok(())
}

#[cfg(test)]
mod tests {
    use super::*;
    use clap::CommandFactory;

    /// clap's own consistency check (duplicate names, bad defaults) over the commands added for the Pro
    /// APIs. It is not run over `agent`: `agent bake --version` already collides with the auto-generated
    /// `--version` (a pre-existing issue that only clap's debug build asserts on).
    #[test]
    fn the_new_commands_are_well_formed() {
        let root = Cli::command();
        for name in ["dataset", "signature", "legal-hold", "key", "secret", "trace", "kitchen", "service-account", "recipe", "budgets", "limits", "cache"] {
            let sub = root.find_subcommand(name).unwrap_or_else(|| panic!("no {name}")).clone();
            sub.debug_assert();
        }
    }

    fn long_help(path: &[&str]) -> String {
        let mut cmd = Cli::command();
        for p in path {
            cmd = cmd.find_subcommand(p).unwrap_or_else(|| panic!("no subcommand {p}")).clone();
        }
        cmd.render_long_help().to_string()
    }

    #[test]
    fn agent_help_says_when_a_version_moves() {
        let help = long_help(&["agent", "versions"]);
        let flat = help.split_whitespace().collect::<Vec<_>>().join(" ");
        assert!(flat.contains("moves only on a bake whose configuration differs"), "{flat}");
        assert!(flat.contains("0.1.0") && flat.contains("do not bump") || flat.contains("does not bump"), "{flat}");
    }

    #[test]
    fn no_help_text_names_the_retired_admin_role_as_a_choice() {
        for path in [vec!["service-account", "create"], vec!["kitchen", "bindings", "add"], vec!["signature", "policy", "set"]] {
            let help = long_help(&path);
            assert!(!help.contains("chef, admin") && !help.contains("(baker, chef, admin)"), "{path:?}: {help}");
        }
        let help = long_help(&["service-account", "create"]);
        for r in ["head_chef", "chef", "baker", "taster", "auditor", "legal", "finance", "viewer"] {
            assert!(help.contains(r), "service-account create help lacks {r}: {help}");
        }
    }

    #[test]
    fn recipe_bake_can_name_an_environment_and_sign_an_approval_skip() {
        let cli = Cli::try_parse_from(["agentoven", "recipe", "bake", "refunds", "--environment", "prod", "--sign"]).unwrap();
        match cli.command {
            Commands::Recipe(recipe::RecipeCommands::Bake(a)) => {
                assert_eq!(a.environment.as_deref(), Some("prod"));
                assert!(a.signing.sign);
            }
            _ => panic!("not recipe bake"),
        }
    }

    #[test]
    fn agent_versions_takes_an_optional_version() {
        for (args, want) in [(vec!["agent", "versions", "bot"], None), (vec!["agent", "versions", "bot", "0.1.0"], Some("0.1.0"))] {
            let mut v = vec!["agentoven"];
            v.extend(args);
            match Cli::try_parse_from(v).unwrap().command {
                Commands::Agent(a) => match *a {
                    agent::AgentCommands::Versions(a) => assert_eq!(a.number.as_deref(), want),
                    _ => panic!(),
                },
                _ => panic!(),
            }
        }
    }
}
