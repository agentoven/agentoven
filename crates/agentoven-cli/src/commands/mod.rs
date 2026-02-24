//! CLI command definitions and dispatch.

pub mod agent;
pub mod dashboard;
pub mod init;
pub mod kitchen;
pub mod login;
pub mod prompt;
pub mod provider;
pub mod rag;
pub mod recipe;
pub mod session;
pub mod tool;
pub mod trace;

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

    /// 🍞 Manage agents in the oven (register, list, bake, invoke, retire).
    #[command(subcommand)]
    Agent(Box<agent::AgentCommands>),

    /// 🔌 Manage model providers (OpenAI, Anthropic, Ollama, etc.).
    #[command(subcommand)]
    Provider(provider::ProviderCommands),

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

    /// 🌐 Start the control plane and open the dashboard UI.
    Dashboard(dashboard::DashboardArgs),

    /// 🔑 Authenticate with the AgentOven control plane.
    Login(login::LoginArgs),

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
    match cli.command {
        Commands::Init(args) => init::execute(args).await,
        Commands::Agent(cmd) => agent::execute(*cmd).await,
        Commands::Provider(cmd) => provider::execute(cmd).await,
        Commands::Tool(cmd) => tool::execute(cmd).await,
        Commands::Prompt(cmd) => prompt::execute(cmd).await,
        Commands::Recipe(cmd) => recipe::execute(cmd).await,
        Commands::Session(cmd) => session::execute(cmd).await,
        Commands::Kitchen(cmd) => kitchen::execute(cmd).await,
        Commands::Trace(cmd) => trace::execute(cmd).await,
        Commands::Rag(cmd) => rag::execute(cmd).await,
        Commands::Dashboard(args) => dashboard::execute(args).await,
        Commands::Login(args) => login::execute(args).await,
        Commands::Status => status().await,
    }
}

async fn status() -> anyhow::Result<()> {
    println!("{BANNER}");

    let client = agentoven_core::AgentOvenClient::from_env()?;

    // Check if control plane is reachable by attempting to list agents
    print!("  Control plane: checking...");
    match client.list_agents().await {
        Ok(agents) => {
            println!("\r  Control plane: 🟢 connected    ");
            println!("  Agents:        {} registered", agents.len());
        }
        Err(_) => {
            println!("\r  Control plane: 🔴 unreachable   ");
            println!("  Tip:           Start with `docker compose up` or set AGENTOVEN_URL");
        }
    }
    println!("  CLI version:   {}", env!("CARGO_PKG_VERSION"));
    Ok(())
}
