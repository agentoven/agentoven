//! Agent — the primary resource in AgentOven.
//!
//! An Agent is a versioned, deployable AI unit registered in the control plane.
//! When baked (deployed), it automatically gets an A2A Agent Card for discovery.

use chrono::{DateTime, Utc};
use serde::{Deserialize, Serialize};
use uuid::Uuid;

use crate::ingredient::Ingredient;

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

    /// Ingredients: models, tools, prompts, and data sources.
    pub ingredients: Vec<Ingredient>,

    /// Tags for categorization and search.
    #[serde(default)]
    pub tags: Vec<String>,

    /// Current deployment status.
    pub status: AgentStatus,

    /// The kitchen (workspace) this agent belongs to.
    #[serde(skip_serializing_if = "Option::is_none")]
    pub kitchen_id: Option<String>,

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
            ingredients: self.ingredients,
            tags: self.tags,
            status: AgentStatus::Draft,
            kitchen_id: self.kitchen_id,
            created_by: None,
            created_at: now,
            updated_at: now,
            metadata: self.metadata,
        }
    }
}

/// The framework used to build an agent.
#[derive(Debug, Clone, Serialize, Deserialize, PartialEq)]
#[serde(rename_all = "kebab-case")]
pub enum AgentFramework {
    LangGraph,
    CrewAi,
    OpenAiSdk,
    AutoGen,
    AgentFramework,
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
            .framework(AgentFramework::LangGraph)
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
