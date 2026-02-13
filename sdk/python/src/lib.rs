use pyo3::prelude::*;

/// AgentOven Python SDK — native Rust bindings via PyO3.
///
/// This module exposes the core AgentOven types and client
/// to Python with zero-copy performance where possible.

// ── Agent Types ──────────────────────────────────────────────

/// Agent status in the AgentOven kitchen.
#[pyclass]
#[derive(Clone, Debug)]
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

#[pyclass]
#[derive(Clone, Debug)]
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
        // TODO: Call the Rust core client via tokio runtime
        Ok(format!("Agent '{}' registered at {}", agent.name, self.url))
    }

    /// List all agents in the current kitchen.
    fn list_agents(&self) -> PyResult<Vec<Agent>> {
        // TODO: Fetch from control plane
        Ok(vec![])
    }

    /// Deploy (bake) an agent.
    fn bake(&self, agent_name: &str) -> PyResult<String> {
        Ok(format!("🔥 Baking agent '{}' in kitchen '{}'", agent_name, self.kitchen))
    }

    /// Pause (cool) an agent.
    fn cool(&self, agent_name: &str) -> PyResult<String> {
        Ok(format!("❄️ Cooling agent '{}'", agent_name))
    }

    fn __repr__(&self) -> String {
        format!("AgentOvenClient(url='{}', kitchen='{}')", self.url, self.kitchen)
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
