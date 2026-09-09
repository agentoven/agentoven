//! `agentoven test` — run agent test cases (community/OSS).

use agentoven_harness::{
    format_json, format_terminal, print_summary, CommunityLocalTestRunner, RunnerConfigBuilder,
    TestSuite,
};
use anyhow::{Context, Result};
use clap::Args;
use std::fs;

#[derive(Args)]
pub struct TestArgs {
    /// Path to test suite JSON file.
    #[arg(value_name = "FILE")]
    pub suite_file: String,

    /// Server URL (if testing against remote server).
    #[arg(long, env = "AGENTOVEN_URL")]
    pub server: Option<String>,

    /// API key for authentication.
    #[arg(long, env = "AGENTOVEN_API_KEY")]
    pub api_key: Option<String>,

    /// Kitchen name.
    #[arg(long, env = "AGENTOVEN_KITCHEN", default_value = "default")]
    pub kitchen: String,

    /// Test case timeout in seconds.
    #[arg(long, default_value = "60")]
    pub timeout: u64,

    /// Verbose output.
    #[arg(short, long)]
    pub verbose: bool,

    /// Output format: terminal or json.
    #[arg(long, default_value = "terminal")]
    pub format: String,

    /// Pretty print JSON output.
    #[arg(long)]
    pub pretty: bool,
}

pub async fn execute(args: TestArgs) -> Result<()> {
    // Load test suite from file
    let content = fs::read_to_string(&args.suite_file)
        .with_context(|| format!("Failed to read test suite file: {}", args.suite_file))?;

    let suite: TestSuite = serde_json::from_str(&content)
        .with_context(|| format!("Failed to parse test suite file: {}", args.suite_file))?;

    // Build runner config
    let mut config_builder = RunnerConfigBuilder::new()
        .kitchen(&args.kitchen)
        .default_timeout(args.timeout)
        .verbose(args.verbose);

    if let Some(server) = args.server {
        config_builder = config_builder.server_url(server);
    }

    if let Some(api_key) = args.api_key {
        config_builder = config_builder.api_key(api_key);
    }

    let config = config_builder.build();

    // Create test runner
    let runner = CommunityLocalTestRunner::new(config)?;

    // Run the test suite
    let report = runner
        .run_suite(&suite.agent, suite.cases, Some(suite.suite))
        .await?;

    // Output results
    match args.format.as_str() {
        "json" => {
            let json = format_json(&report, args.pretty)?;
            println!("{}", json);
        }
        "terminal" | _ => {
            let output = format_terminal(&report, args.verbose);
            println!("{}", output);
            print_summary(&report);
        }
    }

    // Exit with non-zero status if tests failed
    if report.failed > 0 || report.errors > 0 {
        std::process::exit(1);
    }

    Ok(())
}
