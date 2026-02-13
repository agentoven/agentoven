//! Recipe — multi-agent workflows in AgentOven.
//!
//! A Recipe is a DAG (Directed Acyclic Graph) of steps, where each step
//! invokes an agent via A2A protocol. Recipes support:
//! - Sequential and parallel execution
//! - Human-in-the-loop approval gates
//! - Conditional branching
//! - Error handling and retries

use chrono::{DateTime, Utc};
use serde::{Deserialize, Serialize};
use uuid::Uuid;

/// A Recipe — a multi-agent workflow (DAG).
#[derive(Debug, Clone, Serialize, Deserialize)]
pub struct Recipe {
    /// Unique identifier.
    pub id: String,

    /// Human-readable name.
    pub name: String,

    /// Description of what this recipe does.
    pub description: String,

    /// Version of this recipe.
    pub version: String,

    /// Ordered steps in the recipe.
    pub steps: Vec<Step>,

    /// When this recipe was created.
    pub created_at: DateTime<Utc>,

    /// Optional metadata.
    #[serde(default, skip_serializing_if = "Option::is_none")]
    pub metadata: Option<serde_json::Value>,
}

impl Recipe {
    /// Create a new recipe.
    pub fn new(name: impl Into<String>, steps: Vec<Step>) -> Self {
        Self {
            id: Uuid::new_v4().to_string(),
            name: name.into(),
            description: String::new(),
            version: "0.1.0".into(),
            steps,
            created_at: Utc::now(),
            metadata: None,
        }
    }
}

/// A step in a recipe — invokes an agent or a human gate.
#[derive(Debug, Clone, Serialize, Deserialize)]
pub struct Step {
    /// Step identifier (unique within the recipe).
    pub id: String,

    /// Human-readable name for this step.
    pub name: String,

    /// The kind of step.
    pub kind: StepKind,

    /// The agent to invoke (for agent steps).
    #[serde(skip_serializing_if = "Option::is_none")]
    pub agent: Option<String>,

    /// Whether this step can run in parallel with the previous step.
    #[serde(default)]
    pub parallel: bool,

    /// Timeout for this step (e.g., "30s", "5m").
    #[serde(skip_serializing_if = "Option::is_none")]
    pub timeout: Option<String>,

    /// IDs of steps that must complete before this one.
    #[serde(default, skip_serializing_if = "Vec::is_empty")]
    pub depends_on: Vec<String>,

    /// Retry policy.
    #[serde(skip_serializing_if = "Option::is_none")]
    pub retry: Option<RetryPolicy>,

    /// Notification targets for human gates.
    #[serde(default, skip_serializing_if = "Vec::is_empty")]
    pub notify: Vec<String>,

    /// Optional step-level configuration.
    #[serde(default, skip_serializing_if = "Option::is_none")]
    pub config: Option<serde_json::Value>,
}

impl Step {
    /// Create an agent step.
    pub fn agent(name: impl Into<String>, agent_ref: impl Into<String>) -> Self {
        Self {
            id: Uuid::new_v4().to_string(),
            name: name.into(),
            kind: StepKind::Agent,
            agent: Some(agent_ref.into()),
            parallel: false,
            timeout: None,
            depends_on: Vec::new(),
            retry: None,
            notify: Vec::new(),
            config: None,
        }
    }

    /// Create a human approval gate.
    pub fn human_gate(name: impl Into<String>, notify: Vec<String>) -> Self {
        Self {
            id: Uuid::new_v4().to_string(),
            name: name.into(),
            kind: StepKind::HumanGate,
            agent: None,
            parallel: false,
            timeout: None,
            depends_on: Vec::new(),
            retry: None,
            notify,
            config: None,
        }
    }

    /// Create an evaluator step.
    pub fn evaluator(name: impl Into<String>, agent_ref: impl Into<String>) -> Self {
        Self {
            id: Uuid::new_v4().to_string(),
            name: name.into(),
            kind: StepKind::Evaluator,
            agent: Some(agent_ref.into()),
            parallel: false,
            timeout: None,
            depends_on: Vec::new(),
            retry: None,
            notify: Vec::new(),
            config: None,
        }
    }

    /// Set this step as parallel with the previous step.
    pub fn with_parallel(mut self) -> Self {
        self.parallel = true;
        self
    }

    /// Set a timeout.
    pub fn with_timeout(mut self, timeout: impl Into<String>) -> Self {
        self.timeout = Some(timeout.into());
        self
    }

    /// Set dependencies.
    pub fn with_depends_on(mut self, deps: Vec<String>) -> Self {
        self.depends_on = deps;
        self
    }
}

/// The kind of step in a recipe.
#[derive(Debug, Clone, Serialize, Deserialize, PartialEq)]
#[serde(rename_all = "kebab-case")]
pub enum StepKind {
    /// Invoke an agent via A2A.
    Agent,
    /// Human approval gate (A2A INPUT_REQUIRED).
    HumanGate,
    /// Evaluation step.
    Evaluator,
    /// Conditional branch.
    Condition,
    /// Parallel fan-out.
    FanOut,
    /// Join after fan-out.
    FanIn,
}

/// Retry policy for a step.
#[derive(Debug, Clone, Serialize, Deserialize)]
pub struct RetryPolicy {
    /// Maximum number of retries.
    pub max_retries: u32,
    /// Delay between retries in seconds.
    pub delay_seconds: u32,
    /// Whether to use exponential backoff.
    #[serde(default)]
    pub exponential_backoff: bool,
}
