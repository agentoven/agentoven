//! AgentOven CLI — bake production-ready AI agents from the terminal.
//!
//! 🏺 `agentoven` — the command-line interface for the AgentOven control plane.

mod commands;

use clap::Parser;
use commands::{execute, Cli};

#[tokio::main]
async fn main() -> anyhow::Result<()> {
    let cli = Cli::parse();
    execute(cli).await
}
