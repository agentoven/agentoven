//! Message — communication units between agents in A2A.
//!
//! A Message contains one or more Parts (text, file, or structured data)
//! and has a role indicating whether it's from the user (client agent) or
//! the remote agent.

use schemars::JsonSchema;
use serde::{Deserialize, Serialize};
use uuid::Uuid;

/// A message exchanged between agents during a task.
#[derive(Debug, Clone, Serialize, Deserialize, JsonSchema)]
#[serde(rename_all = "camelCase")]
pub struct Message {
    /// Unique identifier for this message.
    pub id: String,

    /// Role of the sender.
    pub role: MessageRole,

    /// Content parts of the message.
    pub parts: Vec<MessagePart>,

    /// Optional metadata.
    #[serde(default, skip_serializing_if = "Option::is_none")]
    pub metadata: Option<serde_json::Value>,
}

impl Message {
    /// Create a message from the user (client agent).
    pub fn user(parts: Vec<MessagePart>) -> Self {
        Self {
            id: Uuid::new_v4().to_string(),
            role: MessageRole::User,
            parts,
            metadata: None,
        }
    }

    /// Create a message from the remote agent.
    pub fn agent(parts: Vec<MessagePart>) -> Self {
        Self {
            id: Uuid::new_v4().to_string(),
            role: MessageRole::Agent,
            parts,
            metadata: None,
        }
    }

    /// Convenience: create a user message with a single text part.
    pub fn user_text(text: impl Into<String>) -> Self {
        Self::user(vec![MessagePart::text(text)])
    }

    /// Convenience: create an agent message with a single text part.
    pub fn agent_text(text: impl Into<String>) -> Self {
        Self::agent(vec![MessagePart::text(text)])
    }

    /// Extract all text content from this message.
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

/// The role of a message sender.
#[derive(Debug, Clone, PartialEq, Eq, Serialize, Deserialize, JsonSchema)]
#[serde(rename_all = "lowercase")]
pub enum MessageRole {
    /// The client agent (sender).
    User,
    /// The remote agent (responder).
    Agent,
}

/// A part of a message — a fully-formed piece of content.
///
/// Each part has a specific type: text, file, or structured data.
#[derive(Debug, Clone, Serialize, Deserialize, JsonSchema)]
#[serde(rename_all = "camelCase", tag = "type")]
pub enum MessagePart {
    /// Plain text content.
    #[serde(rename = "text")]
    Text {
        text: String,
        #[serde(skip_serializing_if = "Option::is_none")]
        media_type: Option<String>,
    },

    /// File content (inline or by reference).
    #[serde(rename = "file")]
    File { file: FilePart },

    /// Structured data (JSON or other).
    #[serde(rename = "data")]
    Data { data: DataPart },
}

impl MessagePart {
    /// Create a text part.
    pub fn text(text: impl Into<String>) -> Self {
        Self::Text {
            text: text.into(),
            media_type: None,
        }
    }

    /// Create a text part with a specific media type.
    pub fn text_with_type(text: impl Into<String>, media_type: impl Into<String>) -> Self {
        Self::Text {
            text: text.into(),
            media_type: Some(media_type.into()),
        }
    }

    /// Create a file part from inline bytes.
    pub fn file_inline(
        name: impl Into<String>,
        media_type: impl Into<String>,
        data: Vec<u8>,
    ) -> Self {
        use base64::Engine;
        Self::File {
            file: FilePart {
                name: Some(name.into()),
                media_type: Some(media_type.into()),
                data: Some(base64::engine::general_purpose::STANDARD.encode(data)),
                url: None,
            },
        }
    }

    /// Create a file part from a URL reference.
    pub fn file_url(url: impl Into<String>, name: Option<String>) -> Self {
        Self::File {
            file: FilePart {
                name,
                media_type: None,
                data: None,
                url: Some(url.into()),
            },
        }
    }

    /// Create a structured data part.
    pub fn data(value: serde_json::Value, media_type: Option<String>) -> Self {
        Self::Data {
            data: DataPart { value, media_type },
        }
    }
}

/// File content — either inline (base64) or by URL reference.
#[derive(Debug, Clone, Serialize, Deserialize, JsonSchema)]
#[serde(rename_all = "camelCase")]
pub struct FilePart {
    /// Optional filename.
    #[serde(skip_serializing_if = "Option::is_none")]
    pub name: Option<String>,

    /// MIME type of the file.
    #[serde(skip_serializing_if = "Option::is_none")]
    pub media_type: Option<String>,

    /// Base64-encoded inline data.
    #[serde(skip_serializing_if = "Option::is_none")]
    pub data: Option<String>,

    /// URL to the file.
    #[serde(skip_serializing_if = "Option::is_none")]
    pub url: Option<String>,
}

/// Structured data content.
#[derive(Debug, Clone, Serialize, Deserialize, JsonSchema)]
#[serde(rename_all = "camelCase")]
pub struct DataPart {
    /// The structured data value.
    pub value: serde_json::Value,

    /// MIME type (e.g., "application/json").
    #[serde(skip_serializing_if = "Option::is_none")]
    pub media_type: Option<String>,
}

#[cfg(test)]
mod tests {
    use super::*;

    #[test]
    fn test_message_creation() {
        let msg = Message::user_text("Hello, summarize this document");
        assert_eq!(msg.role, MessageRole::User);
        assert_eq!(msg.parts.len(), 1);
        assert_eq!(msg.text_content(), "Hello, summarize this document");
    }

    #[test]
    fn test_message_serialization() {
        let msg = Message::user(vec![
            MessagePart::text("Check this file"),
            MessagePart::file_url("https://example.com/doc.pdf", Some("doc.pdf".into())),
            MessagePart::data(
                serde_json::json!({"priority": "high"}),
                Some("application/json".into()),
            ),
        ]);

        let json = serde_json::to_string_pretty(&msg).unwrap();
        let parsed: Message = serde_json::from_str(&json).unwrap();
        assert_eq!(parsed.parts.len(), 3);
    }
}
