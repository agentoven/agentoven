//! CLI command definitions and dispatch.

pub mod agent;
pub mod init;
pub mod login;
pub mod recipe;
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

    /// 🍞 Manage agents in the oven (register, list, bake, retire).
    #[command(subcommand)]
    Agent(agent::AgentCommands),

    /// 📖 Manage recipes (multi-agent workflows).
    #[command(subcommand)]
    Recipe(recipe::RecipeCommands),

    /// 🔍 Inspect traces and observability data.
    #[command(subcommand)]
    Trace(trace::TraceCommands),

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
        Commands::Agent(cmd) => agent::execute(cmd).await,
        Commands::Recipe(cmd) => recipe::execute(cmd).await,
        Commands::Trace(cmd) => trace::execute(cmd).await,
        Commands::Login(args) => login::execute(args).await,
        Commands::Status => status().await,
    }
}

async fn status() -> anyhow::Result<()> {
    println!("{BANNER}");
    println!("  Control plane: checking...");

    let client = agentoven_core::AgentOvenClient::from_env()?;
    // TODO: health check endpoint
    println!("  Status:        🟢 connected");
    println!("  Version:       {}", env!("CARGO_PKG_VERSION"));
    Ok(())
}
