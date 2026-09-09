//! # AgentOven Test Harness
//!
//! A standalone test harness for running test suites against AgentOven agents.
//! This crate can be used independently or integrated into other tools.
//!
//! ## Features
//!
//! - Sequential test execution (community edition)
//! - Multiple match modes: exact, contains, regex, llm_judge
//! - Terminal and JSON output formats
//! - Server and local mode support
//!
//! ## Example
//!
//! ```rust,no_run
//! use agentoven_harness::{
//!     CommunityLocalTestRunner, RunnerConfigBuilder, TestCase, MatchMode,
//! };
//!
//! #[tokio::main]
//! async fn main() -> anyhow::Result<()> {
//!     let config = RunnerConfigBuilder::new()
//!         .server_url("http://localhost:3030")
//!         .api_key("your-api-key")
//!         .kitchen("default")
//!         .build();
//!     
//!     let runner = CommunityLocalTestRunner::new(config)?;
//!     
//!     let cases = vec![
//!         TestCase {
//!             id: "tc-001".to_string(),
//!             name: "basic test".to_string(),
//!             input: "What is 2+2?".to_string(),
//!             expected: "4".to_string(),
//!             match_mode: MatchMode::Contains,
//!             tags: vec![],
//!             timeout_seconds: None,
//!         },
//!     ];
//!     
//!     let report = runner.run_suite("my-agent", cases, None).await?;
//!     println!("Tests: {} passed, {} failed", report.passed, report.failed);
//!     
//!     Ok(())
//! }
//! ```

pub mod matcher;
pub mod models;
pub mod reporter;
pub mod runner;

// Re-export main types for convenience
pub use models::{
    MatchMode, TestCase, TestCaseResult, TestRunReport, TestRunStatus, TestSuite,
};
pub use reporter::{format_json, format_terminal, print_summary};
pub use runner::{CommunityLocalTestRunner, RunnerConfig, RunnerConfigBuilder};
