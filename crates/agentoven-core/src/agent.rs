//! Agent — the primary resource in AgentOven.
//!
//! An Agent is a versioned, deployable AI unit registered in the control plane.
//! When baked (deployed), it automatically gets an A2A Agent Card for discovery.

use chrono::{DateTime, Utc};
use serde::{Deserialize, Serialize};
use uuid::Uuid;

use crate::ingredient::Ingredient;

/// Agent execution mode.
#[derive(Debug, Clone, Default, Serialize, Deserialize, PartialEq)]
#[serde(rename_all = "lowercase")]
pub enum AgentMode {
    /// AgentOven runs the agentic loop (built-in executor).
    #[default]
    Managed,
    /// Agent is external, proxied via A2A endpoint.
    External,
    /// Unknown or unset mode (deserialized from empty string or unrecognized value).
    #[serde(other)]
    Unknown,
}

impl std::fmt::Display for AgentMode {
    fn fmt(&self, f: &mut std::fmt::Formatter<'_>) -> std::fmt::Result {
        match self {
            AgentMode::Managed => write!(f, "managed"),
            AgentMode::External => write!(f, "external"),
            AgentMode::Unknown => write!(f, "unknown"),
        }
    }
}

/// A guardrail applied to an agent's input or output.
#[derive(Debug, Clone, Serialize, Deserialize)]
pub struct Guardrail {
    /// Guardrail kind (e.g., "content-filter", "pii-detector", "token-budget").
    pub kind: String,
    /// When to apply: "pre" (before LLM) or "post" (after LLM).
    #[serde(default = "default_guardrail_stage")]
    pub stage: String,
    /// Kind-specific configuration.
    #[serde(default, skip_serializing_if = "Option::is_none")]
    pub config: Option<serde_json::Value>,
}

fn default_guardrail_stage() -> String {
    "pre".into()
}

/// An Agent registered in AgentOven — a versioned, deployable AI unit.
#[derive(Debug, Clone, Serialize, Deserialize)]
pub struct Agent {
    /// Unique identifier.
    pub id: String,

    /// Human-readable name (must be unique within a kitchen/workspace).
    pub name: String,

    /// Semantic version (e.g., "1.0.0").
    pub version: String,

    /// Description of what this agent does.
    pub description: String,

    /// The framework used to build this agent.
    pub framework: AgentFramework,

    /// Agent mode: managed (AgentOven executor) or external (A2A proxy).
    #[serde(default)]
    pub mode: AgentMode,

    /// Primary model provider name.
    #[serde(default)]
    pub model_provider: String,

    /// Primary model name.
    #[serde(default)]
    pub model_name: String,

    /// Backup model provider for failover.
    #[serde(default, skip_serializing_if = "Option::is_none")]
    pub backup_provider: Option<String>,

    /// Backup model name for failover.
    #[serde(default, skip_serializing_if = "Option::is_none")]
    pub backup_model: Option<String>,

    /// System prompt / instructions.
    #[serde(default, skip_serializing_if = "Option::is_none")]
    pub system_prompt: Option<String>,

    /// Maximum turns for managed agentic loop.
    #[serde(default, skip_serializing_if = "Option::is_none")]
    pub max_turns: Option<u32>,

    /// Agent skills/capabilities.
    #[serde(default)]
    pub skills: Vec<String>,

    /// Guardrails applied to this agent.
    #[serde(default)]
    pub guardrails: Vec<Guardrail>,

    /// A2A endpoint for external agents.
    #[serde(default, skip_serializing_if = "Option::is_none")]
    pub a2a_endpoint: Option<String>,

    /// Ingredients: models, tools, prompts, and data sources.
    #[serde(default)]
    pub ingredients: Vec<Ingredient>,

    /// Tags for categorization and search.
    #[serde(default)]
    pub tags: Vec<String>,

    /// Current deployment status.
    pub status: AgentStatus,

    /// The kitchen (workspace) this agent belongs to.
    /// Accepts both "kitchen" and "kitchen_id" from JSON.
    #[serde(default, alias = "kitchen", skip_serializing_if = "Option::is_none")]
    pub kitchen_id: Option<String>,

    /// Resolved configuration (populated by the control plane after baking).
    #[serde(default, skip_serializing_if = "Option::is_none")]
    pub resolved_config: Option<serde_json::Value>,

    /// Who created this agent.
    #[serde(skip_serializing_if = "Option::is_none")]
    pub created_by: Option<String>,

    /// When this version was created.
    pub created_at: DateTime<Utc>,

    /// When this version was last updated.
    pub updated_at: DateTime<Utc>,

    /// Optional metadata.
    #[serde(default, skip_serializing_if = "Option::is_none")]
    pub metadata: Option<serde_json::Value>,
}

impl Agent {
    /// Start building a new agent.
    pub fn builder(name: impl Into<String>) -> AgentBuilder {
        AgentBuilder::new(name)
    }

    /// Get the fully qualified name: "name@version".
    pub fn qualified_name(&self) -> String {
        format!("{}@{}", self.name, self.version)
    }
}

/// Builder for creating agents with a fluent API.
#[derive(Debug)]
pub struct AgentBuilder {
    name: String,
    version: String,
    description: String,
    framework: AgentFramework,
    mode: AgentMode,
    model_provider: String,
    model_name: String,
    backup_provider: Option<String>,
    backup_model: Option<String>,
    system_prompt: Option<String>,
    max_turns: Option<u32>,
    skills: Vec<String>,
    guardrails: Vec<Guardrail>,
    a2a_endpoint: Option<String>,
    ingredients: Vec<Ingredient>,
    tags: Vec<String>,
    kitchen_id: Option<String>,
    metadata: Option<serde_json::Value>,
}

impl AgentBuilder {
    pub fn new(name: impl Into<String>) -> Self {
        Self {
            name: name.into(),
            version: "0.1.0".into(),
            description: String::new(),
            framework: AgentFramework::Custom,
            mode: AgentMode::default(),
            model_provider: String::new(),
            model_name: String::new(),
            backup_provider: None,
            backup_model: None,
            system_prompt: None,
            max_turns: None,
            skills: Vec::new(),
            guardrails: Vec::new(),
            a2a_endpoint: None,
            ingredients: Vec::new(),
            tags: Vec::new(),
            kitchen_id: None,
            metadata: None,
        }
    }

    pub fn version(mut self, version: impl Into<String>) -> Self {
        self.version = version.into();
        self
    }

    pub fn description(mut self, desc: impl Into<String>) -> Self {
        self.description = desc.into();
        self
    }

    pub fn framework(mut self, framework: AgentFramework) -> Self {
        self.framework = framework;
        self
    }

    pub fn ingredient(mut self, ingredient: Ingredient) -> Self {
        self.ingredients.push(ingredient);
        self
    }

    pub fn tag(mut self, tag: impl Into<String>) -> Self {
        self.tags.push(tag.into());
        self
    }

    pub fn kitchen(mut self, kitchen_id: impl Into<String>) -> Self {
        self.kitchen_id = Some(kitchen_id.into());
        self
    }

    pub fn mode(mut self, mode: AgentMode) -> Self {
        self.mode = mode;
        self
    }

    pub fn model_provider(mut self, provider: impl Into<String>) -> Self {
        self.model_provider = provider.into();
        self
    }

    pub fn model_name(mut self, name: impl Into<String>) -> Self {
        self.model_name = name.into();
        self
    }

    pub fn backup_provider(mut self, provider: impl Into<String>) -> Self {
        self.backup_provider = Some(provider.into());
        self
    }

    pub fn backup_model(mut self, model: impl Into<String>) -> Self {
        self.backup_model = Some(model.into());
        self
    }

    pub fn system_prompt(mut self, prompt: impl Into<String>) -> Self {
        self.system_prompt = Some(prompt.into());
        self
    }

    pub fn max_turns(mut self, turns: u32) -> Self {
        self.max_turns = Some(turns);
        self
    }

    pub fn skill(mut self, s: impl Into<String>) -> Self {
        self.skills.push(s.into());
        self
    }

    pub fn guardrail(mut self, g: Guardrail) -> Self {
        self.guardrails.push(g);
        self
    }

    pub fn a2a_endpoint(mut self, endpoint: impl Into<String>) -> Self {
        self.a2a_endpoint = Some(endpoint.into());
        self
    }

    pub fn metadata(mut self, metadata: serde_json::Value) -> Self {
        self.metadata = Some(metadata);
        self
    }

    pub fn build(self) -> Agent {
        let now = Utc::now();
        Agent {
            id: Uuid::new_v4().to_string(),
            name: self.name,
            version: self.version,
            description: self.description,
            framework: self.framework,
            mode: self.mode,
            model_provider: self.model_provider,
            model_name: self.model_name,
            backup_provider: self.backup_provider,
            backup_model: self.backup_model,
            system_prompt: self.system_prompt,
            max_turns: self.max_turns,
            skills: self.skills,
            guardrails: self.guardrails,
            a2a_endpoint: self.a2a_endpoint,
            ingredients: self.ingredients,
            tags: self.tags,
            status: AgentStatus::Draft,
            kitchen_id: self.kitchen_id,
            resolved_config: None,
            created_by: None,
            created_at: now,
            updated_at: now,
            metadata: self.metadata,
        }
    }
}

/// The framework used to build an agent.
#[derive(Debug, Clone, Default, Serialize, Deserialize, PartialEq)]
#[serde(rename_all = "lowercase")]
pub enum AgentFramework {
    /// LangGraph / LangChain agents.
    #[serde(alias = "lang-graph", alias = "langgraph")]
    Langchain,
    /// CrewAI agents.
    #[serde(alias = "crew-ai")]
    Crewai,
    /// OpenAI SDK agents.
    #[serde(alias = "openai-sdk", alias = "open-ai-sdk")]
    Openai,
    /// AutoGen agents.
    #[serde(alias = "auto-gen")]
    Autogen,
    /// Managed by AgentOven runtime.
    Managed,
    /// Custom / unknown framework.
    #[default]
    #[serde(other)]
    Custom,
}

/// Deployment status of an agent.
#[derive(Debug, Clone, Serialize, Deserialize, PartialEq)]
#[serde(rename_all = "lowercase")]
pub enum AgentStatus {
    /// Agent is in draft — not yet deployed.
    Draft,
    /// Agent is being baked (deploying).
    Baking,
    /// Agent is deployed and serving.
    Ready,
    /// Agent is paused/disabled.
    Cooled,
    /// Agent deployment failed.
    Burnt,
    /// Agent has been retired.
    Retired,
}

impl std::fmt::Display for AgentStatus {
    fn fmt(&self, f: &mut std::fmt::Formatter<'_>) -> std::fmt::Result {
        match self {
            AgentStatus::Draft => write!(f, "🟡 draft"),
            AgentStatus::Baking => write!(f, "🔥 baking"),
            AgentStatus::Ready => write!(f, "🟢 ready"),
            AgentStatus::Cooled => write!(f, "⏸️  cooled"),
            AgentStatus::Burnt => write!(f, "🔴 burnt"),
            AgentStatus::Retired => write!(f, "⚫ retired"),
        }
    }
}

#[cfg(test)]
mod tests {
    use super::*;
    use crate::ingredient::Ingredient;

    #[test]
    fn test_agent_builder() {
        let agent = Agent::builder("summarizer")
            .version("1.0.0")
            .description("Summarizes documents")
            .framework(AgentFramework::Langchain)
            .ingredient(Ingredient::model("gpt-4o").provider("azure-openai").build())
            .tag("nlp")
            .tag("summarization")
            .build();

        assert_eq!(agent.name, "summarizer");
        assert_eq!(agent.version, "1.0.0");
        assert_eq!(agent.qualified_name(), "summarizer@1.0.0");
        assert_eq!(agent.status, AgentStatus::Draft);
        assert_eq!(agent.ingredients.len(), 1);
        assert_eq!(agent.tags, vec!["nlp", "summarization"]);
    }
}
