//! Artifact — outputs/deliverables produced by a task.

use schemars::JsonSchema;
use serde::{Deserialize, Serialize};
use uuid::Uuid;

use crate::message::MessagePart;

/// An artifact produced by a completed task.
///
/// Artifacts contain the deliverables — the actual results of the agent's work.
#[derive(Debug, Clone, Serialize, Deserialize, JsonSchema)]
#[serde(rename_all = "camelCase")]
pub struct Artifact {
    /// Unique identifier for this artifact.
    pub id: String,

    /// Human-readable name/title.
    #[serde(skip_serializing_if = "Option::is_none")]
    pub name: Option<String>,

    /// Description of what this artifact contains.
    #[serde(skip_serializing_if = "Option::is_none")]
    pub description: Option<String>,

    /// The content parts of this artifact.
    pub parts: Vec<MessagePart>,

    /// Optional metadata.
    #[serde(default, skip_serializing_if = "Option::is_none")]
    pub metadata: Option<serde_json::Value>,
}

impl Artifact {
    /// Create a new artifact with text content.
    pub fn text(name: impl Into<String>, content: impl Into<String>) -> Self {
        Self {
            id: Uuid::new_v4().to_string(),
            name: Some(name.into()),
            description: None,
            parts: vec![MessagePart::text(content)],
            metadata: None,
        }
    }

    /// Create a new artifact with structured data.
    pub fn data(name: impl Into<String>, value: serde_json::Value) -> Self {
        Self {
            id: Uuid::new_v4().to_string(),
            name: Some(name.into()),
            description: None,
            parts: vec![MessagePart::data(value, Some("application/json".into()))],
            metadata: None,
        }
    }

    /// Extract text content from this artifact.
    pub fn text_content(&self) -> String {
        self.parts
            .iter()
            .filter_map(|p| match p {
                MessagePart::Text { text, .. } => Some(text.as_str()),
                _ => None,
            })
            .collect::<Vec<_>>()
            .join("\n")
    }
}
