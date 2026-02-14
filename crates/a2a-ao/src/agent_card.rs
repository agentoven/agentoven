//! Agent Card — the self-describing metadata document for agent discovery.
//!
//! Every A2A-compatible agent publishes an Agent Card at:
//!   `/.well-known/agent-card.json`
//!
//! The card describes the agent's capabilities, skills, supported interfaces,
//! security schemes, and input/output modes.

use serde::{Deserialize, Serialize};
use schemars::JsonSchema;
use url::Url;

use crate::error::{A2AError, A2AResult};

/// An A2A Agent Card — metadata describing an agent's capabilities.
///
/// Published at `/.well-known/agent-card.json` for discovery.
#[derive(Debug, Clone, Serialize, Deserialize, JsonSchema)]
#[serde(rename_all = "camelCase")]
pub struct AgentCard {
    /// Human-readable name of the agent.
    pub name: String,

    /// Description of what the agent does.
    pub description: String,

    /// Semantic version of the agent.
    #[serde(skip_serializing_if = "Option::is_none")]
    pub version: Option<String>,

    /// The provider/organization that created this agent.
    #[serde(skip_serializing_if = "Option::is_none")]
    pub provider: Option<AgentProvider>,

    /// URL to the agent's icon/logo.
    #[serde(skip_serializing_if = "Option::is_none")]
    pub icon_url: Option<Url>,

    /// URL to the agent's documentation.
    #[serde(skip_serializing_if = "Option::is_none")]
    pub documentation_url: Option<Url>,

    /// Interfaces supported by this agent (URLs + protocol bindings).
    pub supported_interfaces: Vec<AgentInterface>,

    /// Capabilities declared by this agent.
    #[serde(default)]
    pub capabilities: AgentCapabilities,

    /// Security schemes supported by this agent.
    #[serde(default, skip_serializing_if = "Vec::is_empty")]
    pub security_schemes: Vec<SecurityScheme>,

    /// Security requirements (references to security_schemes).
    #[serde(default, skip_serializing_if = "Vec::is_empty")]
    pub security: Vec<SecurityRequirement>,

    /// Default input content types accepted.
    #[serde(default, skip_serializing_if = "Vec::is_empty")]
    pub default_input_modes: Vec<ContentType>,

    /// Default output content types produced.
    #[serde(default, skip_serializing_if = "Vec::is_empty")]
    pub default_output_modes: Vec<ContentType>,

    /// Skills (specific abilities) of this agent.
    #[serde(default, skip_serializing_if = "Vec::is_empty")]
    pub skills: Vec<AgentSkill>,
}

impl AgentCard {
    /// Discover an agent by fetching its Agent Card from the well-known endpoint.
    ///
    /// Fetches `{base_url}/.well-known/agent-card.json`.
    pub async fn discover(base_url: &str) -> A2AResult<Self> {
        let url = format!(
            "{}/.well-known/agent-card.json",
            base_url.trim_end_matches('/')
        );

        tracing::info!(url = %url, "Discovering A2A agent");

        let response = reqwest::get(&url).await.map_err(|e| {
            A2AError::DiscoveryFailed(format!("Failed to fetch agent card: {e}"))
        })?;

        if !response.status().is_success() {
            return Err(A2AError::DiscoveryFailed(format!(
                "Agent card endpoint returned {}",
                response.status()
            )));
        }

        let card: AgentCard = response.json().await.map_err(|e| {
            A2AError::InvalidAgentCard(format!("Failed to parse agent card: {e}"))
        })?;

        card.validate()?;

        tracing::info!(
            name = %card.name,
            skills = card.skills.len(),
            "Discovered A2A agent"
        );

        Ok(card)
    }

    /// Validate the agent card has required fields.
    pub fn validate(&self) -> A2AResult<()> {
        if self.name.is_empty() {
            return Err(A2AError::InvalidAgentCard("name is required".into()));
        }
        if self.description.is_empty() {
            return Err(A2AError::InvalidAgentCard(
                "description is required".into(),
            ));
        }
        if self.supported_interfaces.is_empty() {
            return Err(A2AError::InvalidAgentCard(
                "at least one supported interface is required".into(),
            ));
        }
        Ok(())
    }

    /// Check if this agent supports streaming.
    pub fn supports_streaming(&self) -> bool {
        self.capabilities.streaming
    }

    /// Check if this agent supports push notifications.
    pub fn supports_push_notifications(&self) -> bool {
        self.capabilities.push_notifications
    }

    /// Find a skill by ID.
    pub fn find_skill(&self, skill_id: &str) -> Option<&AgentSkill> {
        self.skills.iter().find(|s| s.id == skill_id)
    }

    /// Get the primary interface URL.
    pub fn primary_url(&self) -> Option<&Url> {
        self.supported_interfaces.first().map(|i| &i.url)
    }
}

/// Information about the agent's provider/creator.
#[derive(Debug, Clone, Serialize, Deserialize, JsonSchema)]
#[serde(rename_all = "camelCase")]
pub struct AgentProvider {
    /// Name of the organization.
    pub organization: String,

    /// URL of the organization.
    #[serde(skip_serializing_if = "Option::is_none")]
    pub url: Option<Url>,
}

/// A supported interface (endpoint + protocol binding).
#[derive(Debug, Clone, Serialize, Deserialize, JsonSchema)]
#[serde(rename_all = "camelCase")]
pub struct AgentInterface {
    /// The URL of this interface.
    pub url: Url,

    /// Protocol binding (e.g., "jsonrpc+http", "grpc").
    pub protocol_binding: ProtocolBinding,

    /// Protocol version (e.g., "1.0").
    #[serde(skip_serializing_if = "Option::is_none")]
    pub protocol_version: Option<String>,
}

/// Protocol binding for an A2A interface.
#[derive(Debug, Clone, Serialize, Deserialize, JsonSchema, PartialEq)]
#[serde(rename_all = "kebab-case")]
pub enum ProtocolBinding {
    /// JSON-RPC 2.0 over HTTP(S).
    JsonrpcHttp,
    /// gRPC.
    Grpc,
    /// HTTP + JSON (REST-style).
    HttpJson,
    /// Custom binding.
    #[serde(untagged)]
    Custom(String),
}

/// Capabilities declared by the agent.
#[derive(Debug, Clone, Default, Serialize, Deserialize, JsonSchema)]
#[serde(rename_all = "camelCase")]
pub struct AgentCapabilities {
    /// Whether the agent supports SSE streaming.
    #[serde(default)]
    pub streaming: bool,

    /// Whether the agent supports push notifications (webhooks).
    #[serde(default)]
    pub push_notifications: bool,

    /// Whether an extended agent card is available post-authentication.
    #[serde(default)]
    pub extended_agent_card: bool,

    /// Declared extensions.
    #[serde(default, skip_serializing_if = "Vec::is_empty")]
    pub extensions: Vec<AgentExtension>,
}

/// An extension declared by the agent.
#[derive(Debug, Clone, Serialize, Deserialize, JsonSchema)]
#[serde(rename_all = "camelCase")]
pub struct AgentExtension {
    /// URI identifying this extension.
    pub uri: String,

    /// Human-readable description.
    #[serde(skip_serializing_if = "Option::is_none")]
    pub description: Option<String>,

    /// Whether this extension is required for interaction.
    #[serde(default)]
    pub required: bool,
}

/// A specific skill/ability of the agent.
#[derive(Debug, Clone, Serialize, Deserialize, JsonSchema)]
#[serde(rename_all = "camelCase")]
pub struct AgentSkill {
    /// Unique identifier for this skill.
    pub id: String,

    /// Human-readable name.
    pub name: String,

    /// Description of what this skill does.
    pub description: String,

    /// Tags for categorization and search.
    #[serde(default, skip_serializing_if = "Vec::is_empty")]
    pub tags: Vec<String>,

    /// Example prompts that demonstrate this skill.
    #[serde(default, skip_serializing_if = "Vec::is_empty")]
    pub examples: Vec<String>,

    /// Accepted input modes for this skill (overrides agent default).
    #[serde(default, skip_serializing_if = "Vec::is_empty")]
    pub input_modes: Vec<ContentType>,

    /// Output modes for this skill (overrides agent default).
    #[serde(default, skip_serializing_if = "Vec::is_empty")]
    pub output_modes: Vec<ContentType>,
}

/// Content type descriptor.
#[derive(Debug, Clone, Serialize, Deserialize, JsonSchema, PartialEq)]
#[serde(rename_all = "camelCase")]
pub struct ContentType {
    /// MIME type (e.g., "text/plain", "application/json", "image/png").
    pub media_type: String,
}

impl ContentType {
    pub fn text() -> Self {
        Self {
            media_type: "text/plain".into(),
        }
    }
    pub fn json() -> Self {
        Self {
            media_type: "application/json".into(),
        }
    }
    pub fn a2a_json() -> Self {
        Self {
            media_type: "application/a2a+json".into(),
        }
    }
}

/// A security scheme (parity with OpenAPI security schemes).
#[derive(Debug, Clone, Serialize, Deserialize, JsonSchema)]
#[serde(rename_all = "camelCase", tag = "type")]
pub enum SecurityScheme {
    /// API key in header, query, or cookie.
    #[serde(rename = "apiKey")]
    ApiKey {
        name: String,
        #[serde(rename = "in")]
        location: ApiKeyLocation,
        #[serde(skip_serializing_if = "Option::is_none")]
        description: Option<String>,
    },

    /// HTTP authentication (Bearer, Basic, etc.).
    Http {
        scheme: String,
        #[serde(skip_serializing_if = "Option::is_none")]
        bearer_format: Option<String>,
        #[serde(skip_serializing_if = "Option::is_none")]
        description: Option<String>,
    },

    /// OAuth 2.0 flows.
    #[serde(rename = "oauth2")]
    OAuth2 {
        flows: serde_json::Value,
        #[serde(skip_serializing_if = "Option::is_none")]
        description: Option<String>,
    },

    /// OpenID Connect.
    OpenIdConnect {
        open_id_connect_url: Url,
        #[serde(skip_serializing_if = "Option::is_none")]
        description: Option<String>,
    },
}

/// Location for API key security scheme.
#[derive(Debug, Clone, Serialize, Deserialize, JsonSchema, PartialEq)]
#[serde(rename_all = "lowercase")]
pub enum ApiKeyLocation {
    Header,
    Query,
    Cookie,
}

/// A security requirement referencing a named security scheme.
#[derive(Debug, Clone, Serialize, Deserialize, JsonSchema)]
pub struct SecurityRequirement {
    /// The name of the security scheme.
    pub scheme: String,
    /// Required scopes (for OAuth2).
    #[serde(default, skip_serializing_if = "Vec::is_empty")]
    pub scopes: Vec<String>,
}

#[cfg(test)]
mod tests {
    use super::*;

    #[test]
    fn test_serialize_agent_card() {
        let card = AgentCard {
            name: "summarizer".into(),
            description: "Summarizes documents with citations".into(),
            version: Some("1.0.0".into()),
            provider: Some(AgentProvider {
                organization: "AgentOven".into(),
                url: Some(Url::parse("https://agentoven.dev").unwrap()),
            }),
            icon_url: None,
            documentation_url: None,
            supported_interfaces: vec![AgentInterface {
                url: Url::parse("https://agent.example.com/a2a").unwrap(),
                protocol_binding: ProtocolBinding::JsonrpcHttp,
                protocol_version: Some("1.0".into()),
            }],
            capabilities: AgentCapabilities {
                streaming: true,
                push_notifications: true,
                ..Default::default()
            },
            security_schemes: vec![SecurityScheme::Http {
                scheme: "bearer".into(),
                bearer_format: Some("JWT".into()),
                description: None,
            }],
            security: vec![],
            default_input_modes: vec![ContentType::text()],
            default_output_modes: vec![ContentType::text(), ContentType::json()],
            skills: vec![AgentSkill {
                id: "summarize".into(),
                name: "Document Summarization".into(),
                description: "Summarizes long documents into concise summaries".into(),
                tags: vec!["summarization".into(), "nlp".into()],
                examples: vec!["Summarize this quarterly report".into()],
                input_modes: vec![],
                output_modes: vec![],
            }],
        };

        let json = serde_json::to_string_pretty(&card).unwrap();
        assert!(json.contains("summarizer"));
        assert!(json.contains("agentoven.dev"));

        // Round-trip
        let parsed: AgentCard = serde_json::from_str(&json).unwrap();
        assert_eq!(parsed.name, "summarizer");
        assert!(parsed.capabilities.streaming);
    }

    #[test]
    fn test_validate_agent_card() {
        let mut card = AgentCard {
            name: "".into(),
            description: "test".into(),
            version: None,
            provider: None,
            icon_url: None,
            documentation_url: None,
            supported_interfaces: vec![],
            capabilities: Default::default(),
            security_schemes: vec![],
            security: vec![],
            default_input_modes: vec![],
            default_output_modes: vec![],
            skills: vec![],
        };

        assert!(card.validate().is_err());

        card.name = "test-agent".into();
        assert!(card.validate().is_err()); // still missing interfaces

        card.supported_interfaces.push(AgentInterface {
            url: Url::parse("https://example.com").unwrap(),
            protocol_binding: ProtocolBinding::JsonrpcHttp,
            protocol_version: None,
        });
        assert!(card.validate().is_ok());
    }
}
