use napi::bindgen_prelude::*;
use napi_derive::napi;

/// AgentOven TypeScript SDK — native Rust bindings via napi-rs.
///
/// Provides zero-overhead native performance for Node.js/Bun.

// ── Agent Status ─────────────────────────────────────────────

#[napi(string_enum)]
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
        // TODO: Call the Rust core client via tokio runtime
        Ok(format!("Agent '{}' registered at {}", agent.name, self.url))
    }

    /// List all agents in the current kitchen.
    #[napi]
    pub async fn list_agents(&self) -> Result<Vec<Agent>> {
        // TODO: Fetch from control plane
        Ok(vec![])
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
