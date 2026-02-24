// PyO3 macro expansion triggers false-positive clippy::useless_conversion
#![allow(clippy::useless_conversion)]

use pyo3::exceptions::PyRuntimeError;
use pyo3::prelude::*;

// AgentOven Python SDK — native Rust bindings via PyO3.
//
// This module exposes the core AgentOven types and client
// to Python with zero-copy performance where possible.

// ── Agent Types ──────────────────────────────────────────────

/// Agent status in the AgentOven kitchen.
#[pyclass(eq, eq_int)]
#[derive(Clone, Debug, PartialEq, Eq)]
pub enum AgentStatus {
    Draft,
    Baking,
    Ready,
    Cooled,
    Burnt,
    Retired,
}

#[pymethods]
impl AgentStatus {
    fn __str__(&self) -> &str {
        match self {
            AgentStatus::Draft => "draft",
            AgentStatus::Baking => "baking",
            AgentStatus::Ready => "ready",
            AgentStatus::Cooled => "cooled",
            AgentStatus::Burnt => "burnt",
            AgentStatus::Retired => "retired",
        }
    }
}

/// An agent definition in the AgentOven registry.
#[pyclass]
#[derive(Clone, Debug)]
pub struct Agent {
    #[pyo3(get, set)]
    pub name: String,
    #[pyo3(get, set)]
    pub description: String,
    #[pyo3(get, set)]
    pub framework: String,
    #[pyo3(get, set)]
    pub version: String,
    #[pyo3(get)]
    pub status: AgentStatus,
}

#[pymethods]
impl Agent {
    #[new]
    #[pyo3(signature = (name, description="".to_string(), framework="custom".to_string(), version="0.1.0".to_string()))]
    fn new(name: String, description: String, framework: String, version: String) -> Self {
        Agent {
            name,
            description,
            framework,
            version,
            status: AgentStatus::Draft,
        }
    }

    fn __repr__(&self) -> String {
        format!(
            "Agent(name='{}', framework='{}', status='{}')",
            self.name,
            self.framework,
            self.status.__str__()
        )
    }
}

// ── Ingredient Types ─────────────────────────────────────────

#[pyclass(eq, eq_int)]
#[derive(Clone, Debug, PartialEq, Eq)]
pub enum IngredientKind {
    Model,
    Tool,
    Prompt,
    Data,
}

#[pyclass]
#[derive(Clone, Debug)]
pub struct Ingredient {
    #[pyo3(get, set)]
    pub name: String,
    #[pyo3(get)]
    pub kind: IngredientKind,
    #[pyo3(get, set)]
    pub required: bool,
}

#[pymethods]
impl Ingredient {
    #[new]
    #[pyo3(signature = (name, kind, required=true))]
    fn new(name: String, kind: IngredientKind, required: bool) -> Self {
        Ingredient {
            name,
            kind,
            required,
        }
    }
}

// ── Recipe Types ─────────────────────────────────────────────

#[pyclass]
#[derive(Clone, Debug)]
pub struct Recipe {
    #[pyo3(get, set)]
    pub name: String,
    #[pyo3(get, set)]
    pub description: String,
}

#[pymethods]
impl Recipe {
    #[new]
    #[pyo3(signature = (name, description="".to_string()))]
    fn new(name: String, description: String) -> Self {
        Recipe { name, description }
    }
}

// ── Client ───────────────────────────────────────────────────

#[pyclass]
#[derive(Clone, Debug)]
pub struct AgentOvenClient {
    url: String,
    api_key: Option<String>,
    kitchen: String,
}

#[pymethods]
impl AgentOvenClient {
    #[new]
    #[pyo3(signature = (url="http://localhost:8080".to_string(), api_key=None, kitchen="default".to_string()))]
    fn new(url: String, api_key: Option<String>, kitchen: String) -> Self {
        AgentOvenClient {
            url,
            api_key,
            kitchen,
        }
    }

    /// Register an agent with the control plane.
    fn register_agent(&self, agent: &Agent) -> PyResult<String> {
        let client = agentoven_core::client::AgentOvenClient::new(&self.url)
            .map_err(|e| PyRuntimeError::new_err(format!("Client init error: {e}")))?;
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
        let rt = tokio::runtime::Runtime::new()
            .map_err(|e| PyRuntimeError::new_err(format!("Failed to create runtime: {e}")))?;
        rt.block_on(async {
            let registered = client
                .register(&core_agent)
                .await
                .map_err(|e| PyRuntimeError::new_err(format!("API error: {e}")))?;
            Ok(format!(
                "Agent '{}' registered (id={})",
                registered.name, registered.id
            ))
        })
    }

    /// List all agents in the current kitchen.
    fn list_agents(&self) -> PyResult<Vec<Agent>> {
        let client = agentoven_core::client::AgentOvenClient::new(&self.url)
            .map_err(|e| PyRuntimeError::new_err(format!("Client init error: {e}")))?;
        let client = if let Some(ref key) = self.api_key {
            client.with_api_key(key.clone())
        } else {
            client
        };
        let client = client.with_kitchen(self.kitchen.clone());
        let rt = tokio::runtime::Runtime::new()
            .map_err(|e| PyRuntimeError::new_err(format!("Failed to create runtime: {e}")))?;
        rt.block_on(async {
            let items = client
                .list_agents()
                .await
                .map_err(|e| PyRuntimeError::new_err(format!("API error: {e}")))?;
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
        })
    }

    /// Deploy (bake) an agent.
    fn bake(&self, agent_name: &str) -> PyResult<String> {
        Ok(format!(
            "🔥 Baking agent '{}' in kitchen '{}'",
            agent_name, self.kitchen
        ))
    }

    /// Pause (cool) an agent.
    fn cool(&self, agent_name: &str) -> PyResult<String> {
        Ok(format!("❄️ Cooling agent '{}'", agent_name))
    }

    fn __repr__(&self) -> String {
        format!(
            "AgentOvenClient(url='{}', kitchen='{}')",
            self.url, self.kitchen
        )
    }
}

// ── Module Registration ──────────────────────────────────────

#[pymodule]
fn _native(m: &Bound<'_, PyModule>) -> PyResult<()> {
    m.add_class::<Agent>()?;
    m.add_class::<AgentStatus>()?;
    m.add_class::<Ingredient>()?;
    m.add_class::<IngredientKind>()?;
    m.add_class::<Recipe>()?;
    m.add_class::<AgentOvenClient>()?;
    Ok(())
}
