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
#[serde(rename_all = "snake_case")]
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
    /// Search the knowledge base.
    Rag,
    /// Route to one of several steps.
    Router,
    /// Run an agent once per item of a list.
    Map,
    /// Run another recipe as a step.
    SubRecipe,
    /// Pause for a length of time, or until a time.
    Wait,
    /// Push a message to channels and tools, then carry on.
    Notify,
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

// ── Recipe documents → what the control plane runs ───────────

/// The kinds of step the engine runs, as it spells them.
pub const ENGINE_KINDS: &[&str] = &[
    "agent", "evaluator", "rag", "human_gate", "condition", "router", "map", "sub_recipe", "fan_out", "fan_in", "wait", "notify",
];

/// A step kind as written in a recipe document, spelled the way the engine spells it. Hyphens and
/// underscores are interchangeable, and a few short names are accepted (`gate`, `branch`, `parallel`, `join`).
pub fn engine_kind(written: &str) -> Option<&'static str> {
    let k = written.trim().to_ascii_lowercase().replace('-', "_");
    let k = match k.as_str() {
        "gate" => "human_gate",
        "branch" => "condition",
        "parallel" => "fan_out",
        "join" => "fan_in",
        "subrecipe" => "sub_recipe",
        other => other,
    };
    ENGINE_KINDS.iter().copied().find(|e| *e == k)
}

/// Seconds from `"30s"`, `"5m"`, `"2h"`, or a bare number of seconds.
pub fn parse_timeout_secs(t: &str) -> Option<u64> {
    let t = t.trim();
    let (digits, factor) = match t.chars().last()? {
        's' => (&t[..t.len() - 1], 1),
        'm' => (&t[..t.len() - 1], 60),
        'h' => (&t[..t.len() - 1], 3600),
        c if c.is_ascii_digit() => (t, 1),
        _ => return None,
    };
    digits.trim().parse::<u64>().ok().map(|n| n * factor)
}

/// Keys of a step document the engine reads as they are.
const PASSTHROUGH: &[&str] = &[
    "depends_on", "default_next", "loop_condition", "max_iterations", "source_path", "max_concurrency",
    "recipe_ref", "input_mapping", "output_mapping", "approver_emails", "approver_domain", "auth_key_ref",
];

/// Step documents, as written in a YAML or JSON recipe, as the steps the control plane stores and runs.
///
/// The field names are the engine's (`agent_ref`, `timeout_secs`, `max_retries`, `notify_tools`, a branch's
/// `next_step`), and `condition: "<expression>"` becomes the condition step's `config.expression`. A step
/// kind or timeout that cannot be read is an error. Keys that are understood by nothing are returned as
/// warnings, so a setting that would be ignored is never ignored silently.
pub fn steps_from_documents(docs: &[serde_json::Value]) -> anyhow::Result<(Vec<serde_json::Value>, Vec<String>)> {
    use serde_json::{json, Map, Value};
    const READ: &[&str] = &[
        "name", "kind", "agent", "agent_ref", "timeout", "timeout_secs", "retries", "max_retries", "condition", "routes",
        "branches", "sub_recipe", "notify", "notify_tools", "config",
    ];
    let mut steps = Vec::new();
    let mut warnings = Vec::new();
    for (i, doc) in docs.iter().enumerate() {
        let obj = doc.as_object().ok_or_else(|| anyhow::anyhow!("step {} is not a mapping", i + 1))?;
        let name = obj
            .get("name")
            .and_then(Value::as_str)
            .filter(|n| !n.trim().is_empty())
            .ok_or_else(|| anyhow::anyhow!("step {} has no name", i + 1))?;
        let written = obj.get("kind").and_then(Value::as_str);
        let kind = match written {
            Some(k) => engine_kind(k).ok_or_else(|| {
                anyhow::anyhow!("step '{name}': unknown kind {k:?}; use one of {}", ENGINE_KINDS.join(", "))
            })?,
            None if obj.contains_key("sub_recipe") || obj.contains_key("recipe_ref") => "sub_recipe",
            None => "agent",
        };
        let mut step = Map::new();
        step.insert("name".into(), json!(name));
        step.insert("kind".into(), json!(kind));
        if let Some(agent) = obj.get("agent_ref").or_else(|| obj.get("agent")).and_then(Value::as_str) {
            step.insert("agent_ref".into(), json!(agent));
            step.insert("agent".into(), json!(agent));
        }
        for key in PASSTHROUGH {
            if let Some(v) = obj.get(*key) {
                step.insert((*key).into(), v.clone());
            }
        }
        if let Some(r) = obj.get("sub_recipe").and_then(Value::as_str) {
            step.insert("recipe_ref".into(), json!(r));
        }
        if let Some(secs) = obj.get("timeout_secs").and_then(Value::as_u64) {
            step.insert("timeout_secs".into(), json!(secs));
        } else if let Some(t) = obj.get("timeout") {
            let text = t.as_str().map(str::to_string).unwrap_or_else(|| t.to_string());
            let secs = parse_timeout_secs(&text)
                .ok_or_else(|| anyhow::anyhow!("step '{name}': timeout {text:?} is not like \"30s\", \"5m\" or \"2h\""))?;
            step.insert("timeout_secs".into(), json!(secs));
        }
        if let Some(n) = obj.get("max_retries").or_else(|| obj.get("retries")).and_then(Value::as_u64) {
            step.insert("max_retries".into(), json!(n));
        }
        if let Some(list) = obj.get("notify_tools").or_else(|| obj.get("notify")).and_then(Value::as_array) {
            step.insert("notify_tools".into(), Value::Array(list.clone()));
        }
        let mut config = obj.get("config").and_then(Value::as_object).cloned().unwrap_or_default();
        if let Some(expr) = obj.get("condition").and_then(Value::as_str) {
            config.insert("expression".into(), json!(expr));
        }
        if !config.is_empty() {
            step.insert("config".into(), Value::Object(config));
        }
        if let Some(routes) = obj.get("branches").or_else(|| obj.get("routes")).and_then(Value::as_array) {
            let mut branches = Vec::new();
            for r in routes {
                let condition = r.get("condition").and_then(Value::as_str);
                let next = ["next_step", "target", "next"].iter().find_map(|k| r.get(*k).and_then(Value::as_str));
                match (condition, next) {
                    (Some(c), Some(n)) => branches.push(json!({"condition": c, "next_step": n})),
                    _ => anyhow::bail!("step '{name}': each route needs a condition and a target step"),
                }
            }
            step.insert("branches".into(), Value::Array(branches));
        }
        for key in obj.keys() {
            if !READ.contains(&key.as_str()) && !PASSTHROUGH.contains(&key.as_str()) {
                warnings.push(format!("step '{name}': '{key}' is not a recipe step setting and was ignored"));
            }
        }
        steps.push(Value::Object(step));
    }
    Ok((steps, warnings))
}

#[cfg(test)]
mod document_tests {
    use super::*;
    use serde_json::json;

    fn one(doc: serde_json::Value) -> serde_json::Value {
        steps_from_documents(&[doc]).unwrap().0.remove(0)
    }

    #[test]
    fn kinds_are_spelled_the_way_the_engine_spells_them() {
        for (written, want) in [("human-gate", "human_gate"), ("gate", "human_gate"), ("human_gate", "human_gate"), ("fan-out", "fan_out"),
            ("parallel", "fan_out"), ("join", "fan_in"), ("branch", "condition"), ("sub-recipe", "sub_recipe"), ("map", "map"), ("router", "router"), ("rag", "rag"), ("wait", "wait"), ("notify", "notify")] {
            assert_eq!(engine_kind(written), Some(want), "{written}");
        }
        assert_eq!(engine_kind("nonsense"), None);
    }

    #[test]
    fn an_unknown_kind_is_an_error_not_an_agent_step() {
        let err = steps_from_documents(&[json!({"name": "x", "kind": "gaet"})]).unwrap_err().to_string();
        assert!(err.contains("unknown kind") && err.contains("human_gate"), "{err}");
    }

    #[test]
    fn an_agent_step_keeps_its_timeout_and_retries() {
        let s = one(json!({"name": "draft", "agent": "writer", "timeout_secs": 30, "retries": 2, "depends_on": ["a"]}));
        assert_eq!(s["agent_ref"], "writer");
        assert_eq!(s["agent"], "writer");
        assert_eq!(s["timeout_secs"], 30);
        assert_eq!(s["max_retries"], 2);
        assert_eq!(s["depends_on"], json!(["a"]));
        assert_eq!(one(json!({"name": "x", "agent": "w", "timeout": "2m"}))["timeout_secs"], 120);
        assert!(steps_from_documents(&[json!({"name": "x", "timeout": "soon"})]).is_err());
    }

    #[test]
    fn a_condition_step_gets_its_expression_and_routes_become_branches() {
        let s = one(json!({"name": "pick", "kind": "condition", "condition": "lower(text) contains \"refund\"",
            "routes": [{"condition": "a == 1", "target": "one"}], "default_next": "two"}));
        assert_eq!(s["config"]["expression"], "lower(text) contains \"refund\"");
        assert_eq!(s["branches"], json!([{"condition": "a == 1", "next_step": "one"}]));
        assert_eq!(s["default_next"], "two");
        assert!(steps_from_documents(&[json!({"name": "p", "routes": [{"condition": "a"}]})]).is_err());
    }

    #[test]
    fn a_human_gate_keeps_its_settings() {
        let s = one(json!({"name": "ok", "kind": "human-gate", "notify": ["slack"], "approver_domain": "corp.com",
            "config": {"max_wait_minutes": 60, "on_reject": "branch"}}));
        assert_eq!(s["kind"], "human_gate");
        assert_eq!(s["notify_tools"], json!(["slack"]));
        assert_eq!(s["approver_domain"], "corp.com");
        assert_eq!(s["config"], json!({"max_wait_minutes": 60, "on_reject": "branch"}));
    }

    #[test]
    fn wait_and_notify_steps_keep_their_settings() {
        let s = one(json!({"name": "pause", "kind": "wait", "config": {"duration": "5m"}}));
        assert_eq!(s["kind"], "wait");
        assert_eq!(s["config"], json!({"duration": "5m"}));
        let n = one(json!({"name": "tell", "kind": "notify", "notify": ["pager"],
            "config": {"message": "Run {{run_id}} needs you", "channels": ["ops"], "on_failure": "carry_on"}}));
        assert_eq!(n["kind"], "notify");
        assert_eq!(n["notify_tools"], json!(["pager"]));
        assert_eq!(n["config"]["channels"], json!(["ops"]));
    }

    #[test]
    fn a_sub_recipe_is_recognised_by_its_target() {
        let s = one(json!({"name": "again", "sub_recipe": "other"}));
        assert_eq!(s["kind"], "sub_recipe");
        assert_eq!(s["recipe_ref"], "other");
    }

    #[test]
    fn a_setting_nothing_reads_is_reported() {
        let (_, warnings) = steps_from_documents(&[json!({"name": "x", "agent": "a", "parallel_inputs": ["a"]})]).unwrap();
        assert_eq!(warnings.len(), 1);
        assert!(warnings[0].contains("parallel_inputs"));
    }
}
