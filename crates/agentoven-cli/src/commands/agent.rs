//! `agentoven agent` — manage agents in the oven.

use clap::{Args, Subcommand};
use colored::Colorize;

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

    // TODO: Parse agentoven.toml and register with control plane
    let client = agentoven_core::AgentOvenClient::from_env()?;

    println!("  {} Agent registered successfully!", "✓".green().bold());
    println!(
        "  {} View in the menu: {}",
        "→".dimmed(),
        "agentoven agent get <name>".green()
    );
    Ok(())
}

async fn list(_args: ListArgs) -> anyhow::Result<()> {
    println!("\n  {} Agents in the kitchen:\n", "📋".to_string());

    let client = agentoven_core::AgentOvenClient::from_env()?;

    // TODO: Fetch and display agents
    println!(
        "  {}  {}  {}  {}",
        "NAME".bold(),
        "VERSION".bold(),
        "STATUS".bold(),
        "FRAMEWORK".bold()
    );
    println!("  {}", "─".repeat(50).dimmed());
    println!("  (no agents registered yet — use `agentoven agent register`)");
    Ok(())
}

async fn get(args: GetArgs) -> anyhow::Result<()> {
    println!("\n  {} Agent: {}\n", "🍞".to_string(), args.name.bold());
    // TODO: Fetch and display agent details
    Ok(())
}

async fn bake(args: BakeArgs) -> anyhow::Result<()> {
    println!(
        "\n  {} Baking {} to {}...\n",
        "🔥".to_string(),
        args.name.bold(),
        args.environment.cyan()
    );

    // TODO: Deploy the agent
    println!("  {} Agent is now baking!", "✓".green().bold());
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
    Ok(())
}

async fn cool(args: CoolArgs) -> anyhow::Result<()> {
    println!(
        "\n  {} Cooling down {}...",
        "❄️".to_string(),
        args.name.bold()
    );
    // TODO: Pause agent
    println!("  {} Agent cooled.", "✓".green().bold());
    Ok(())
}

async fn retire(args: RetireArgs) -> anyhow::Result<()> {
    println!(
        "\n  {} Retiring {}...",
        "⚫".to_string(),
        args.name.bold()
    );
    // TODO: Retire agent
    println!("  {} Agent retired from the menu.", "✓".green().bold());
    Ok(())
}

async fn test(args: TestArgs) -> anyhow::Result<()> {
    println!(
        "\n  {} Testing {} in the playground...\n",
        "🧪".to_string(),
        args.name.bold()
    );

    println!("  Type a message and press Enter. Type {} to quit.\n", "/exit".dimmed());

    // TODO: Interactive agent testing via A2A
    if let Some(msg) = args.message {
        println!("  {} {}", "You:".bold(), msg);
        println!("  {} (connecting to agent...)", "Agent:".bold().cyan());
    }
    Ok(())
}
