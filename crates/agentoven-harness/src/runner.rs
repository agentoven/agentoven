//! Test runner implementation for executing test suites against agents.

use crate::matcher::evaluate_match;
use crate::models::{TestCase, TestCaseResult, TestRunReport};
use agentoven_core::client::AgentOvenClient;
use anyhow::{Context, Result};
use std::time::{Duration, Instant};
use uuid::Uuid;

/// Configuration for the test runner
#[derive(Debug, Clone)]
pub struct RunnerConfig {
    /// AgentOven server URL (None for local-only mode)
    pub server_url: Option<String>,
    
    /// API key for authentication
    pub api_key: Option<String>,
    
    /// Kitchen name
    pub kitchen: String,
    
    /// Default timeout per test case in seconds
    pub default_timeout: u64,
    
    /// Verbose output
    pub verbose: bool,
}

impl Default for RunnerConfig {
    fn default() -> Self {
        Self {
            server_url: None,
            api_key: None,
            kitchen: "default".to_string(),
            default_timeout: 60,
            verbose: false,
        }
    }
}

/// Community local test runner
pub struct CommunityLocalTestRunner {
    config: RunnerConfig,
    client: Option<AgentOvenClient>,
}

impl CommunityLocalTestRunner {
    /// Create a new test runner with the given configuration
    pub fn new(config: RunnerConfig) -> Result<Self> {
        let client = if let Some(ref url) = config.server_url {
            let mut client = AgentOvenClient::new(url)?
                .with_kitchen(&config.kitchen);
            
            if let Some(ref api_key) = config.api_key {
                client = client.with_api_key(api_key);
            }
            Some(client)
        } else {
            None
        };
        
        Ok(Self { config, client })
    }
    
    /// Run a test suite and return the report
    pub async fn run_suite(
        &self,
        agent_name: &str,
        cases: Vec<TestCase>,
        suite_id: Option<String>,
    ) -> Result<TestRunReport> {
        let run_id = Uuid::new_v4().to_string();
        let mut report = TestRunReport::new(run_id, agent_name.to_string(), suite_id);
        
        let start_time = Instant::now();
        
        // Run each test case sequentially (community edition = no parallelism)
        for case in cases {
            let result = self.run_case(agent_name, &case).await;
            report.add_result(result);
        }
        
        report.finalize(start_time.elapsed());
        Ok(report)
    }
    
    /// Run a single test case
    async fn run_case(&self, agent_name: &str, case: &TestCase) -> TestCaseResult {
        let start = Instant::now();
        
        // Apply timeout
        let timeout = Duration::from_secs(
            case.timeout_seconds.unwrap_or(self.config.default_timeout)
        );
        
        // Call the agent
        let actual = match tokio::time::timeout(
            timeout,
            self.invoke_agent(agent_name, &case.input),
        )
        .await
        {
            Ok(Ok(response)) => response,
            Ok(Err(e)) => {
                // Agent invocation failed
                return TestCaseResult {
                    case_id: case.id.clone(),
                    case_name: case.name.clone(),
                    input: case.input.clone(),
                    expected: case.expected.clone(),
                    actual: String::new(),
                    passed: false,
                    match_mode: case.match_mode,
                    score: None,
                    reason: None,
                    duration_ms: start.elapsed().as_millis() as u64,
                    error: Some(format!("Agent invocation failed: {}", e)),
                };
            }
            Err(_) => {
                // Timeout
                return TestCaseResult {
                    case_id: case.id.clone(),
                    case_name: case.name.clone(),
                    input: case.input.clone(),
                    expected: case.expected.clone(),
                    actual: String::new(),
                    passed: false,
                    match_mode: case.match_mode,
                    score: None,
                    reason: None,
                    duration_ms: start.elapsed().as_millis() as u64,
                    error: Some(format!("Test case timed out after {}s", timeout.as_secs())),
                };
            }
        };
        
        // Evaluate the result
        // For now, we don't support LLM judge in the harness itself
        // (would require model configuration, etc.)
        evaluate_match(
            case.id.clone(),
            case.name.clone(),
            case.input.clone(),
            case.expected.clone(),
            actual,
            case.match_mode,
            None, // LLM judge not supported in community runner yet
        )
        .await
    }
    
    /// Invoke an agent with the given input
    async fn invoke_agent(&self, agent_name: &str, input: &str) -> Result<String> {
        if let Some(ref client) = self.client {
            // Server mode: call the agent via HTTP
            let response = client
                .invoke_agent(agent_name, input, None, false)
                .await
                .context("Failed to invoke agent")?;
            
            // Extract the text response
            if let Some(text) = response.get("response").and_then(|v| v.as_str()) {
                Ok(text.to_string())
            } else {
                // Try to get it from message format
                if let Some(text) = response
                    .get("message")
                    .and_then(|m| m.get("content"))
                    .and_then(|c| c.as_str())
                {
                    Ok(text.to_string())
                } else {
                    // Fallback: return JSON string
                    Ok(response.to_string())
                }
            }
        } else {
            // Local mode: not implemented yet
            // Would require running agents locally without a server
            anyhow::bail!(
                "Local-only mode (without server) is not yet implemented. \
                 Please specify --server and --api-key to test against a running server."
            )
        }
    }
}

/// Builder for RunnerConfig
pub struct RunnerConfigBuilder {
    config: RunnerConfig,
}

impl RunnerConfigBuilder {
    pub fn new() -> Self {
        Self {
            config: RunnerConfig::default(),
        }
    }
    
    pub fn server_url(mut self, url: impl Into<String>) -> Self {
        self.config.server_url = Some(url.into());
        self
    }
    
    pub fn api_key(mut self, key: impl Into<String>) -> Self {
        self.config.api_key = Some(key.into());
        self
    }
    
    pub fn kitchen(mut self, kitchen: impl Into<String>) -> Self {
        self.config.kitchen = kitchen.into();
        self
    }
    
    pub fn default_timeout(mut self, seconds: u64) -> Self {
        self.config.default_timeout = seconds;
        self
    }
    
    pub fn verbose(mut self, verbose: bool) -> Self {
        self.config.verbose = verbose;
        self
    }
    
    pub fn build(self) -> RunnerConfig {
        self.config
    }
}

impl Default for RunnerConfigBuilder {
    fn default() -> Self {
        Self::new()
    }
}
