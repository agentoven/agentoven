//! `agentoven trace` — inspect traces and observability data.

use clap::{Args, Subcommand};
use colored::Colorize;

#[derive(Subcommand)]
pub enum TraceCommands {
    /// List recent traces.
    Ls(TraceLsArgs),
    /// Inspect a specific trace.
    Get(TraceGetArgs),
    /// Show cost summary.
    Cost(CostArgs),
}

#[derive(Args)]
pub struct TraceLsArgs {
    /// Filter by agent name.
    #[arg(long, short)]
    pub agent: Option<String>,
    /// Number of traces to show.
    #[arg(long, short, default_value = "20")]
    pub limit: u32,
}

#[derive(Args)]
pub struct TraceGetArgs {
    /// Trace ID.
    pub trace_id: String,
}

#[derive(Args)]
pub struct CostArgs {
    /// Time range (e.g., "24h", "7d", "30d").
    #[arg(long, short, default_value = "24h")]
    pub range: String,
    /// Group by agent, model, or kitchen.
    #[arg(long, short, default_value = "agent")]
    pub group_by: String,
}

pub async fn execute(cmd: TraceCommands) -> anyhow::Result<()> {
    match cmd {
        TraceCommands::Ls(args) => {
            println!("\n  {} Recent traces (last {}):\n", "🔍".to_string(), args.limit);
            println!(
                "  {}  {}  {}  {}  {}",
                "TRACE ID".bold(),
                "AGENT".bold(),
                "STATUS".bold(),
                "LATENCY".bold(),
                "COST".bold(),
            );
            println!("  {}", "─".repeat(65).dimmed());
            println!("  (no traces yet — traces appear when agents are invoked)");
            Ok(())
        }
        TraceCommands::Get(args) => {
            println!("\n  {} Trace: {}\n", "🔍".to_string(), args.trace_id.bold());
            // TODO: Fetch and display trace waterfall
            Ok(())
        }
        TraceCommands::Cost(args) => {
            println!(
                "\n  {} Cost summary (last {}, by {}):\n",
                "💰".to_string(),
                args.range.cyan(),
                args.group_by
            );
            // TODO: Fetch and display cost data
            println!("  Total: $0.00");
            Ok(())
        }
    }
}
