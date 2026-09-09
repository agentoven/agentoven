//! Test case models and types for the AgentOven test harness.
//!
//! Based on ADR-0011: OSS Local Test Runner for Agentic Agents

use chrono::{DateTime, Utc};
use serde::{Deserialize, Serialize};
use std::time::Duration;

/// Test case definition
#[derive(Debug, Clone, Serialize, Deserialize)]
pub struct TestCase {
    /// Unique identifier for the test case
    pub id: String,
    
    /// Human-readable name
    pub name: String,
    
    /// Input to send to the agent
    pub input: String,
    
    /// Expected output (interpretation depends on match_mode)
    pub expected: String,
    
    /// How to match the actual output against expected
    #[serde(default = "default_match_mode")]
    pub match_mode: MatchMode,
    
    /// Optional tags for filtering/categorization
    #[serde(default, skip_serializing_if = "Vec::is_empty")]
    pub tags: Vec<String>,
    
    /// Per-case timeout in seconds (optional)
    #[serde(default, skip_serializing_if = "Option::is_none")]
    pub timeout_seconds: Option<u64>,
}

fn default_match_mode() -> MatchMode {
    MatchMode::Exact
}

/// Match mode determines how to compare expected vs actual output
#[derive(Debug, Clone, Copy, PartialEq, Eq, Serialize, Deserialize)]
#[serde(rename_all = "snake_case")]
pub enum MatchMode {
    /// String equality (trimmed whitespace)
    Exact,
    
    /// Expected is substring of actual (case-insensitive)
    Contains,
    
    /// Expected is regex pattern, tested against actual
    Regex,
    
    /// LLM evaluates (input, expected, actual) → score + reason
    LlmJudge,
}

/// Result of running a single test case
#[derive(Debug, Clone, Serialize, Deserialize)]
pub struct TestCaseResult {
    /// Test case ID
    pub case_id: String,
    
    /// Test case name
    pub case_name: String,
    
    /// Input sent to agent
    pub input: String,
    
    /// Expected output
    pub expected: String,
    
    /// Actual output from agent
    pub actual: String,
    
    /// Whether the test passed
    pub passed: bool,
    
    /// Match mode used
    pub match_mode: MatchMode,
    
    /// Score (0.0-1.0) for LLM judge mode
    #[serde(default, skip_serializing_if = "Option::is_none")]
    pub score: Option<f64>,
    
    /// LLM judge explanation
    #[serde(default, skip_serializing_if = "Option::is_none")]
    pub reason: Option<String>,
    
    /// Duration of test case execution
    pub duration_ms: u64,
    
    /// Error message if test case failed to execute
    #[serde(default, skip_serializing_if = "Option::is_none")]
    pub error: Option<String>,
}

/// Complete test run report
#[derive(Debug, Clone, Serialize, Deserialize)]
pub struct TestRunReport {
    /// Unique run ID
    pub run_id: String,
    
    /// Test suite ID (if from suite)
    #[serde(default, skip_serializing_if = "Option::is_none")]
    pub suite_id: Option<String>,
    
    /// Agent name being tested
    pub agent_name: String,
    
    /// Overall status
    pub status: TestRunStatus,
    
    /// Total number of test cases
    pub total: usize,
    
    /// Number passed
    pub passed: usize,
    
    /// Number failed
    pub failed: usize,
    
    /// Number with errors (couldn't execute)
    pub errors: usize,
    
    /// Individual test case results
    pub results: Vec<TestCaseResult>,
    
    /// When the run started
    pub started_at: DateTime<Utc>,
    
    /// Total duration
    pub duration_ms: u64,
}

/// Test run status
#[derive(Debug, Clone, Copy, PartialEq, Eq, Serialize, Deserialize)]
#[serde(rename_all = "snake_case")]
pub enum TestRunStatus {
    /// Test run is in progress
    Running,
    
    /// All tests passed
    Passed,
    
    /// Some tests failed
    Failed,
    
    /// Test run encountered an error and couldn't complete
    Error,
}

/// Test suite definition (file format)
#[derive(Debug, Clone, Serialize, Deserialize)]
pub struct TestSuite {
    /// Suite name
    pub suite: String,
    
    /// Agent to test
    pub agent: String,
    
    /// Test cases
    pub cases: Vec<TestCase>,
}

impl TestRunReport {
    /// Create a new test run report
    pub fn new(run_id: String, agent_name: String, suite_id: Option<String>) -> Self {
        Self {
            run_id,
            suite_id,
            agent_name,
            status: TestRunStatus::Running,
            total: 0,
            passed: 0,
            failed: 0,
            errors: 0,
            results: Vec::new(),
            started_at: Utc::now(),
            duration_ms: 0,
        }
    }
    
    /// Add a test case result and update stats
    pub fn add_result(&mut self, result: TestCaseResult) {
        self.total += 1;
        
        if result.error.is_some() {
            self.errors += 1;
        } else if result.passed {
            self.passed += 1;
        } else {
            self.failed += 1;
        }
        
        self.results.push(result);
    }
    
    /// Finalize the report with duration and status
    pub fn finalize(&mut self, duration: Duration) {
        self.duration_ms = duration.as_millis() as u64;
        
        self.status = if self.errors > 0 {
            TestRunStatus::Error
        } else if self.failed > 0 {
            TestRunStatus::Failed
        } else {
            TestRunStatus::Passed
        };
    }
}
