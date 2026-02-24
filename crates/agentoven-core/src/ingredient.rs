//! Ingredient — models, tools, prompts, and data sources that go into an agent.
//!
//! In AgentOven's kitchen vocabulary, ingredients are the building blocks
//! that get combined to bake an agent.

use serde::{Deserialize, Serialize};

/// An ingredient used by an agent — a model, tool, prompt, or data source.
#[derive(Debug, Clone, Serialize, Deserialize)]
pub struct Ingredient {
    /// The kind of ingredient.
    pub kind: IngredientKind,

    /// Name/identifier of the ingredient (e.g., "gpt-4o", "doc-reader").
    pub name: String,

    /// Provider or protocol (e.g., "azure-openai", "mcp").
    #[serde(skip_serializing_if = "Option::is_none")]
    pub provider: Option<String>,

    /// Role in the agent (e.g., "primary", "fallback", "evaluator").
    #[serde(skip_serializing_if = "Option::is_none")]
    pub role: Option<String>,

    /// Configuration specific to this ingredient.
    #[serde(default, skip_serializing_if = "Option::is_none")]
    pub config: Option<serde_json::Value>,
}

/// Builder for creating ingredients with a fluent API.
#[derive(Debug)]
pub struct IngredientBuilder {
    kind: IngredientKind,
    name: String,
    provider: Option<String>,
    role: Option<String>,
    config: Option<serde_json::Value>,
}

impl IngredientBuilder {
    pub fn provider(mut self, provider: impl Into<String>) -> Self {
        self.provider = Some(provider.into());
        self
    }

    pub fn role(mut self, role: impl Into<String>) -> Self {
        self.role = Some(role.into());
        self
    }

    pub fn config(mut self, config: serde_json::Value) -> Self {
        self.config = Some(config);
        self
    }

    pub fn build(self) -> Ingredient {
        Ingredient {
            kind: self.kind,
            name: self.name,
            provider: self.provider,
            role: self.role,
            config: self.config,
        }
    }
}

impl Ingredient {
    /// Create a model ingredient.
    pub fn model(name: impl Into<String>) -> IngredientBuilder {
        IngredientBuilder {
            kind: IngredientKind::Model,
            name: name.into(),
            provider: None,
            role: None,
            config: None,
        }
    }

    /// Create a tool ingredient.
    pub fn tool(name: impl Into<String>) -> IngredientBuilder {
        IngredientBuilder {
            kind: IngredientKind::Tool,
            name: name.into(),
            provider: None,
            role: None,
            config: None,
        }
    }

    /// Create a prompt ingredient.
    pub fn prompt(name: impl Into<String>) -> IngredientBuilder {
        IngredientBuilder {
            kind: IngredientKind::Prompt,
            name: name.into(),
            provider: None,
            role: None,
            config: None,
        }
    }

    /// Create a data source ingredient.
    pub fn data(name: impl Into<String>) -> IngredientBuilder {
        IngredientBuilder {
            kind: IngredientKind::Data,
            name: name.into(),
            provider: None,
            role: None,
            config: None,
        }
    }

    /// Create an observability ingredient.
    pub fn observability(name: impl Into<String>) -> IngredientBuilder {
        IngredientBuilder {
            kind: IngredientKind::Observability,
            name: name.into(),
            provider: None,
            role: None,
            config: None,
        }
    }

    /// Create an embedding ingredient.
    pub fn embedding(name: impl Into<String>) -> IngredientBuilder {
        IngredientBuilder {
            kind: IngredientKind::Embedding,
            name: name.into(),
            provider: None,
            role: None,
            config: None,
        }
    }

    /// Create a vector store ingredient.
    pub fn vectorstore(name: impl Into<String>) -> IngredientBuilder {
        IngredientBuilder {
            kind: IngredientKind::VectorStore,
            name: name.into(),
            provider: None,
            role: None,
            config: None,
        }
    }

    /// Create a retriever ingredient.
    pub fn retriever(name: impl Into<String>) -> IngredientBuilder {
        IngredientBuilder {
            kind: IngredientKind::Retriever,
            name: name.into(),
            provider: None,
            role: None,
            config: None,
        }
    }
}

/// The kind of ingredient.
#[derive(Debug, Clone, Serialize, Deserialize, PartialEq)]
#[serde(rename_all = "lowercase")]
pub enum IngredientKind {
    /// An LLM model (e.g., GPT-4o, Claude, Llama).
    Model,
    /// A tool accessible via MCP or API.
    Tool,
    /// A prompt template.
    Prompt,
    /// A data source (vector store, database, file).
    Data,
    /// Observability configuration.
    Observability,
    /// Embedding model configuration.
    Embedding,
    /// Vector store backend configuration.
    #[serde(rename = "vectorstore")]
    VectorStore,
    /// Retriever pipeline configuration.
    Retriever,
}
