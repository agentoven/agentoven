use napi::bindgen_prelude::*;
use napi_derive::napi;

/// AgentOven TypeScript SDK — native Rust bindings via napi-rs.
///
/// Provides zero-overhead native performance for Node.js/Bun.

// ── Agent Status ─────────────────────────────────────────────

#[napi(string_enum)]
#[derive(Debug)]
pub enum AgentStatus {
    Draft,
    Baking,
    Ready,
    Cooled,
    Burnt,
    Retired,
}

// ── Agent ────────────────────────────────────────────────────

#[napi(object)]
#[derive(Clone, Debug)]
pub struct Agent {
    pub name: String,
    pub description: String,
    pub framework: String,
    pub version: String,
    pub status: AgentStatus,
}

#[napi]
pub fn create_agent(
    name: String,
    description: Option<String>,
    framework: Option<String>,
    version: Option<String>,
) -> Agent {
    Agent {
        name,
        description: description.unwrap_or_default(),
        framework: framework.unwrap_or_else(|| "custom".to_string()),
        version: version.unwrap_or_else(|| "0.1.0".to_string()),
        status: AgentStatus::Draft,
    }
}

// ── Ingredient Kind ──────────────────────────────────────────

#[napi(string_enum)]
#[derive(Debug)]
pub enum IngredientKind {
    Model,
    Tool,
    Prompt,
    Data,
}

#[napi(object)]
#[derive(Clone, Debug)]
pub struct Ingredient {
    pub name: String,
    pub kind: IngredientKind,
    pub required: bool,
}

// ── Recipe ───────────────────────────────────────────────────

#[napi(object)]
#[derive(Clone, Debug)]
pub struct Recipe {
    pub name: String,
    pub description: String,
}

// ── Client ───────────────────────────────────────────────────

#[napi]
pub struct AgentOvenClient {
    url: String,
    api_key: Option<String>,
    kitchen: String,
}

#[napi]
impl AgentOvenClient {
    #[napi(constructor)]
    pub fn new(url: Option<String>, api_key: Option<String>, kitchen: Option<String>) -> Self {
        AgentOvenClient {
            url: url.unwrap_or_else(|| "http://localhost:8080".to_string()),
            api_key,
            kitchen: kitchen.unwrap_or_else(|| "default".to_string()),
        }
    }

    /// Register an agent with the control plane.
    #[napi]
    pub async fn register_agent(&self, agent: Agent) -> Result<String> {
        let client = agentoven_core::client::AgentOvenClient::new(&self.url)
            .map_err(|e| Error::from_reason(format!("Client init error: {e}")))?;
        let client = if let Some(ref key) = self.api_key {
            client.with_api_key(key.clone())
        } else {
            client
        };
        let client = client.with_kitchen(self.kitchen.clone());
        let core_agent = agentoven_core::agent::Agent::builder(&agent.name)
            .version(&agent.version)
            .description(&agent.description)
            .build();
        let registered = client
            .register(&core_agent)
            .await
            .map_err(|e| Error::from_reason(format!("API error: {e}")))?;
        Ok(format!("Agent '{}' registered (id={})", registered.name, registered.id))
    }

    /// List all agents in the current kitchen.
    #[napi]
    pub async fn list_agents(&self) -> Result<Vec<Agent>> {
        let client = agentoven_core::client::AgentOvenClient::new(&self.url)
            .map_err(|e| Error::from_reason(format!("Client init error: {e}")))?;
        let client = if let Some(ref key) = self.api_key {
            client.with_api_key(key.clone())
        } else {
            client
        };
        let client = client.with_kitchen(self.kitchen.clone());
        let items = client
            .list_agents()
            .await
            .map_err(|e| Error::from_reason(format!("API error: {e}")))?;
        let agents = items
            .into_iter()
            .map(|a| Agent {
                name: a.name,
                description: a.description,
                framework: format!("{:?}", a.framework),
                version: a.version,
                status: match a.status {
                    agentoven_core::agent::AgentStatus::Baking => AgentStatus::Baking,
                    agentoven_core::agent::AgentStatus::Ready => AgentStatus::Ready,
                    agentoven_core::agent::AgentStatus::Cooled => AgentStatus::Cooled,
                    agentoven_core::agent::AgentStatus::Burnt => AgentStatus::Burnt,
                    agentoven_core::agent::AgentStatus::Retired => AgentStatus::Retired,
                    _ => AgentStatus::Draft,
                },
            })
            .collect();
        Ok(agents)
    }

    /// Deploy (bake) an agent.
    #[napi]
    pub async fn bake(&self, agent_name: String) -> Result<String> {
        Ok(format!(
            "🔥 Baking agent '{}' in kitchen '{}'",
            agent_name, self.kitchen
        ))
    }

    /// Pause (cool) an agent.
    #[napi]
    pub async fn cool(&self, agent_name: String) -> Result<String> {
        Ok(format!("❄️ Cooling agent '{}'", agent_name))
    }
}
