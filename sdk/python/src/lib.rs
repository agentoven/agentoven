// PyO3 macro expansion triggers false-positive clippy::useless_conversion
#![allow(clippy::useless_conversion)]

use pyo3::exceptions::PyRuntimeError;
use pyo3::prelude::*;
use pyo3::types::{PyDict, PyList};

// AgentOven Python SDK — native Rust bindings via PyO3.
//
// Fluent API design:
//
//   from agentoven import Agent, Ingredient, Recipe, Step, AgentOvenClient
//
//   agent = Agent("summarizer", ingredients=[
//       Ingredient.model("gpt-4o", provider="azure-openai"),
//       Ingredient.tool("doc-reader", protocol="mcp"),
//   ])
//
//   client = AgentOvenClient()
//   client.register(agent)
//   client.bake(agent)

// ── Agent Status ─────────────────────────────────────────────

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

    fn __repr__(&self) -> String {
        format!("AgentStatus.{}", self.__str__().to_uppercase())
    }
}

// ── Ingredient Kind ──────────────────────────────────────────

#[pyclass(eq, eq_int)]
#[derive(Clone, Debug, PartialEq, Eq)]
pub enum IngredientKind {
    Model,
    Tool,
    Prompt,
    Data,
}

#[pymethods]
impl IngredientKind {
    fn __str__(&self) -> &str {
        match self {
            IngredientKind::Model => "model",
            IngredientKind::Tool => "tool",
            IngredientKind::Prompt => "prompt",
            IngredientKind::Data => "data",
        }
    }
}

// ── Ingredient ───────────────────────────────────────────────

/// An ingredient for an agent — a model, tool, prompt, or data source.
///
/// Create via class methods for a fluent API:
///
///     Ingredient.model("gpt-4o", provider="azure-openai")
///     Ingredient.model("claude-sonnet", provider="anthropic", role="fallback")
///     Ingredient.tool("doc-reader", protocol="mcp")
///     Ingredient.prompt("system", text="You are a helpful assistant.")
///     Ingredient.data("knowledge-base")
#[pyclass]
#[derive(Clone, Debug)]
pub struct Ingredient {
    #[pyo3(get)]
    pub kind: IngredientKind,
    #[pyo3(get, set)]
    pub name: String,
    #[pyo3(get, set)]
    pub provider: Option<String>,
    #[pyo3(get, set)]
    pub role: Option<String>,
    #[pyo3(get, set)]
    pub protocol: Option<String>,
    #[pyo3(get, set)]
    pub required: bool,
    /// Config stored as a JSON string internally.
    #[pyo3(get, set)]
    pub config: Option<String>,
}

#[pymethods]
impl Ingredient {
    /// Low-level constructor — prefer Ingredient.model/tool/prompt/data instead.
    #[new]
    #[pyo3(signature = (name, kind, required=true, provider=None, role=None, protocol=None, config=None))]
    fn new(
        name: String,
        kind: IngredientKind,
        required: bool,
        provider: Option<String>,
        role: Option<String>,
        protocol: Option<String>,
        config: Option<&Bound<'_, PyAny>>,
    ) -> PyResult<Self> {
        let config_str = pyobj_to_json_string(config)?;
        Ok(Ingredient { kind, name, provider, role, protocol, required, config: config_str })
    }

    /// Create a model ingredient.
    ///
    ///     Ingredient.model("gpt-4o", provider="azure-openai")
    ///     Ingredient.model("claude-sonnet", provider="anthropic", role="fallback")
    #[staticmethod]
    #[pyo3(signature = (name, provider=None, role=None, config=None))]
    fn model(name: String, provider: Option<String>, role: Option<String>, config: Option<&Bound<'_, PyAny>>) -> PyResult<Self> {
        let config_str = pyobj_to_json_string(config)?;
        Ok(Ingredient { kind: IngredientKind::Model, name, provider, role, protocol: None, required: true, config: config_str })
    }

    /// Create a tool ingredient.
    ///
    ///     Ingredient.tool("doc-reader", protocol="mcp")
    ///     Ingredient.tool("web-search", provider="tavily")
    #[staticmethod]
    #[pyo3(signature = (name, protocol=None, provider=None, config=None))]
    fn tool(name: String, protocol: Option<String>, provider: Option<String>, config: Option<&Bound<'_, PyAny>>) -> PyResult<Self> {
        let config_str = pyobj_to_json_string(config)?;
        Ok(Ingredient { kind: IngredientKind::Tool, name, provider, role: None, protocol, required: true, config: config_str })
    }

    /// Create a prompt ingredient.
    ///
    ///     Ingredient.prompt("system", text="You are a helpful assistant.")
    #[staticmethod]
    #[pyo3(signature = (name, text=None, config=None))]
    fn prompt(name: String, text: Option<String>, config: Option<&Bound<'_, PyAny>>) -> PyResult<Self> {
        let config_str = if config.is_some() {
            pyobj_to_json_string(config)?
        } else {
            text.map(|t| serde_json::json!({"text": t}).to_string())
        };
        Ok(Ingredient { kind: IngredientKind::Prompt, name, provider: None, role: None, protocol: None, required: true, config: config_str })
    }

    /// Create a data source ingredient.
    ///
    ///     Ingredient.data("knowledge-base", provider="pinecone")
    #[staticmethod]
    #[pyo3(signature = (name, provider=None, config=None))]
    fn data(name: String, provider: Option<String>, config: Option<&Bound<'_, PyAny>>) -> PyResult<Self> {
        let config_str = pyobj_to_json_string(config)?;
        Ok(Ingredient { kind: IngredientKind::Data, name, provider, role: None, protocol: None, required: true, config: config_str })
    }

    fn __repr__(&self) -> String {
        let mut parts = vec![format!("'{}'", self.name)];
        if let Some(ref p) = self.provider { parts.push(format!("provider='{p}'")); }
        if let Some(ref r) = self.role { parts.push(format!("role='{r}'")); }
        if let Some(ref p) = self.protocol { parts.push(format!("protocol='{p}'")); }
        format!("Ingredient.{}({})", self.kind.__str__(), parts.join(", "))
    }
}

/// Convert an optional Python object (dict/str/etc.) to an optional JSON string.
fn pyobj_to_json_string(obj: Option<&Bound<'_, PyAny>>) -> PyResult<Option<String>> {
    match obj {
        None => Ok(None),
        Some(o) => {
            // If it's a dict, convert key/value pairs to JSON
            if let Ok(dict) = o.downcast::<PyDict>() {
                let mut map = serde_json::Map::new();
                for (k, v) in dict.iter() {
                    let key: String = k.extract()?;
                    // Try string first, fallback to repr
                    let val = if let Ok(s) = v.extract::<String>() {
                        serde_json::Value::String(s)
                    } else if let Ok(b) = v.extract::<bool>() {
                        serde_json::Value::Bool(b)
                    } else if let Ok(i) = v.extract::<i64>() {
                        serde_json::Value::Number(i.into())
                    } else if let Ok(f) = v.extract::<f64>() {
                        serde_json::json!(f)
                    } else {
                        serde_json::Value::String(v.repr()?.to_string())
                    };
                    map.insert(key, val);
                }
                Ok(Some(serde_json::Value::Object(map).to_string()))
            } else if let Ok(s) = o.extract::<String>() {
                Ok(Some(s))
            } else {
                Ok(Some(o.repr()?.to_string()))
            }
        }
    }
}

/// Convert a Python object to a serde_json::Value, handling nested dicts and lists.
fn pyany_to_json(obj: &Bound<'_, PyAny>) -> PyResult<serde_json::Value> {
    // Check bool before i64 because Python bool is a subclass of int
    if let Ok(b) = obj.extract::<bool>() {
        return Ok(serde_json::Value::Bool(b));
    }
    if let Ok(i) = obj.extract::<i64>() {
        return Ok(serde_json::Value::Number(i.into()));
    }
    if let Ok(f) = obj.extract::<f64>() {
        return Ok(serde_json::json!(f));
    }
    if let Ok(s) = obj.extract::<String>() {
        return Ok(serde_json::Value::String(s));
    }
    if obj.is_none() {
        return Ok(serde_json::Value::Null);
    }
    if let Ok(dict) = obj.downcast::<PyDict>() {
        let mut map = serde_json::Map::new();
        for (k, v) in dict.iter() {
            let key: String = k.extract()?;
            map.insert(key, pyany_to_json(&v)?);
        }
        return Ok(serde_json::Value::Object(map));
    }
    if let Ok(list) = obj.downcast::<PyList>() {
        let mut arr = Vec::new();
        for item in list.iter() {
            arr.push(pyany_to_json(&item)?);
        }
        return Ok(serde_json::Value::Array(arr));
    }
    Ok(serde_json::Value::String(obj.repr()?.to_string()))
}

// ── Step ─────────────────────────────────────────────────────

/// A step in a multi-agent recipe.
///
///     Step("planner", agent="task-planner", timeout="30s")
///     Step("researcher", agent="doc-researcher", parallel=True)
///     Step("approval", human_gate=True, notify=["team-leads"])
#[pyclass]
#[derive(Clone, Debug)]
pub struct Step {
    #[pyo3(get, set)]
    pub name: String,
    #[pyo3(get, set)]
    pub kind: String,
    #[pyo3(get, set)]
    pub agent: Option<String>,
    #[pyo3(get, set)]
    pub parallel: bool,
    #[pyo3(get, set)]
    pub timeout: Option<String>,
    #[pyo3(get, set)]
    pub human_gate: bool,
    #[pyo3(get, set)]
    pub notify: Vec<String>,
    #[pyo3(get, set)]
    pub depends_on: Vec<String>,
}

#[pymethods]
impl Step {
    #[new]
    #[pyo3(signature = (name, kind="agent".to_string(), agent=None, parallel=false, timeout=None, human_gate=false, notify=vec![], depends_on=vec![]))]
    fn new(
        name: String,
        kind: String,
        agent: Option<String>,
        parallel: bool,
        timeout: Option<String>,
        human_gate: bool,
        notify: Vec<String>,
        depends_on: Vec<String>,
    ) -> Self {
        Step { name, kind, agent, parallel, timeout, human_gate, notify, depends_on }
    }

    fn __repr__(&self) -> String {
        if self.human_gate {
            format!("Step('{}', human_gate=True)", self.name)
        } else if let Some(ref a) = self.agent {
            format!("Step('{}', agent='{}')", self.name, a)
        } else {
            format!("Step('{}')", self.name)
        }
    }
}

// ── Agent ────────────────────────────────────────────────────

/// An agent definition in the AgentOven registry.
///
///     agent = Agent("summarizer",
///         description="Summarizes documents",
///         ingredients=[
///             Ingredient.model("gpt-4o", provider="azure-openai"),
///             Ingredient.tool("doc-reader", protocol="mcp"),
///         ],
///     )
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
    #[pyo3(get, set)]
    pub model_provider: String,
    #[pyo3(get, set)]
    pub model_name: String,
    #[pyo3(get, set)]
    pub mode: String,
    #[pyo3(get, set)]
    pub system_prompt: Option<String>,
    #[pyo3(get, set)]
    pub ingredients: Vec<Ingredient>,
    #[pyo3(get)]
    pub status: AgentStatus,
}

#[pymethods]
impl Agent {
    #[new]
    #[pyo3(signature = (
        name,
        description="".to_string(),
        framework="custom".to_string(),
        version="0.1.0".to_string(),
        model_provider="".to_string(),
        model_name="".to_string(),
        mode="managed".to_string(),
        system_prompt=None,
        ingredients=vec![],
    ))]
    #[allow(clippy::too_many_arguments)]
    fn new(
        name: String,
        description: String,
        framework: String,
        version: String,
        model_provider: String,
        model_name: String,
        mode: String,
        system_prompt: Option<String>,
        ingredients: Vec<Ingredient>,
    ) -> Self {
        Agent { name, description, framework, version, model_provider, model_name, mode, system_prompt, ingredients, status: AgentStatus::Draft }
    }

    /// Add an ingredient to this agent.
    ///
    ///     agent.add_ingredient(Ingredient.model("gpt-4o", provider="openai"))
    fn add_ingredient(&mut self, ingredient: Ingredient) {
        self.ingredients.push(ingredient);
    }

    fn __repr__(&self) -> String {
        let mut parts = vec![
            format!("name='{}'", self.name),
            format!("framework='{}'", self.framework),
            format!("status='{}'", self.status.__str__()),
        ];
        if !self.ingredients.is_empty() {
            parts.push(format!("ingredients={}", self.ingredients.len()));
        }
        format!("Agent({})", parts.join(", "))
    }
}

// ── Recipe ───────────────────────────────────────────────────

/// A multi-agent workflow (DAG of steps).
///
///     recipe = Recipe("document-review",
///         steps=[
///             Step("planner", agent="task-planner", timeout="30s"),
///             Step("researcher", agent="doc-researcher", parallel=True),
///             Step("approval", human_gate=True, notify=["team-leads"]),
///         ],
///     )
#[pyclass]
#[derive(Clone, Debug)]
pub struct Recipe {
    #[pyo3(get, set)]
    pub name: String,
    #[pyo3(get, set)]
    pub description: String,
    #[pyo3(get, set)]
    pub version: String,
    #[pyo3(get, set)]
    pub steps: Vec<Step>,
}

#[pymethods]
impl Recipe {
    #[new]
    #[pyo3(signature = (name, description="".to_string(), version="0.1.0".to_string(), steps=vec![]))]
    fn new(name: String, description: String, version: String, steps: Vec<Step>) -> Self {
        Recipe { name, description, version, steps }
    }

    fn __repr__(&self) -> String {
        format!("Recipe(name='{}', steps={})", self.name, self.steps.len())
    }
}

// ── Client ───────────────────────────────────────────────────

/// The AgentOven control-plane client.
///
///     client = AgentOvenClient()
///     client.register(agent)
///     client.bake(agent)          # bake an agent
///     client.bake(recipe)         # bake a recipe
///     client.cool(agent)
///     client.rewarm(agent)
#[pyclass]
#[derive(Clone, Debug)]
pub struct AgentOvenClient {
    url: String,
    api_key: Option<String>,
    kitchen: String,
}

impl AgentOvenClient {
    // ── Internal helpers ─────────────────────────────────────

    fn api_url(&self, path: &str) -> String {
        let base = self.url.trim_end_matches('/');
        format!("{base}/api/v1{path}")
    }

    fn authed_request(
        &self,
        http: &reqwest::blocking::Client,
        method: reqwest::Method,
        url: &str,
    ) -> reqwest::blocking::RequestBuilder {
        let mut req = http.request(method, url);
        if let Some(ref key) = self.api_key {
            req = req.header("Authorization", format!("Bearer {key}"));
        }
        req = req.header("X-Kitchen", &self.kitchen);
        req
    }

    fn send(&self, req: reqwest::blocking::RequestBuilder) -> PyResult<(u16, String)> {
        let resp = req
            .send()
            .map_err(|e| PyRuntimeError::new_err(format!("Request failed: {e}")))?;
        let status = resp.status().as_u16();
        let body = resp.text().unwrap_or_default();
        Ok((status, body))
    }

    /// Serialize an Agent into the control-plane JSON shape.
    fn agent_to_json(&self, agent: &Agent) -> serde_json::Value {
        let mut ingredients = Vec::new();
        for ing in &agent.ingredients {
            let mut obj = serde_json::json!({
                "kind": ing.kind.__str__(),
                "name": ing.name,
                "required": ing.required,
            });
            if let Some(ref p) = ing.provider {
                obj["provider"] = serde_json::json!(p);
            }
            if let Some(ref r) = ing.role {
                obj["role"] = serde_json::json!(r);
            }
            if let Some(ref p) = ing.protocol {
                obj["config"] = serde_json::json!({ "protocol": p });
            }
            // Merge JSON config string if provided
            if let Some(ref cfg_str) = ing.config {
                if let Ok(parsed) = serde_json::from_str::<serde_json::Value>(cfg_str) {
                    if let serde_json::Value::Object(cfg_map) = parsed {
                        if let serde_json::Value::Object(ref mut map) = obj {
                            let config_entry = map.entry("config").or_insert_with(|| serde_json::json!({}));
                            if let serde_json::Value::Object(ref mut cm) = config_entry {
                                for (k, v) in cfg_map {
                                    cm.insert(k, v);
                                }
                            }
                        }
                    }
                }
            }
            // Auto-populate model config from top-level fields
            if ing.kind == IngredientKind::Model {
                let provider = ing.provider.as_deref().unwrap_or(&agent.model_provider);
                let model = if ing.name.is_empty() { &agent.model_name } else { &ing.name };
                if let serde_json::Value::Object(ref mut map) = obj {
                    let config_map = map.entry("config").or_insert_with(|| serde_json::json!({}));
                    if let serde_json::Value::Object(ref mut cm) = config_map {
                        if !provider.is_empty() {
                            cm.entry("provider").or_insert_with(|| serde_json::json!(provider));
                        }
                        if !model.is_empty() {
                            cm.entry("model").or_insert_with(|| serde_json::json!(model));
                        }
                    }
                }
            }
            ingredients.push(obj);
        }

        let mut body = serde_json::json!({
            "name": agent.name,
            "description": agent.description,
            "framework": agent.framework,
            "version": agent.version,
            "ingredients": ingredients,
        });

        // Agent mode
        if !agent.mode.is_empty() {
            body["mode"] = serde_json::json!(agent.mode);
        }
        // Top-level model fields for backward compat
        if !agent.model_provider.is_empty() {
            body["model_provider"] = serde_json::json!(agent.model_provider);
        }
        if !agent.model_name.is_empty() {
            body["model_name"] = serde_json::json!(agent.model_name);
        }
        // Convert system_prompt → prompt ingredient (backend reads ingredients, not top-level field)
        if let Some(ref sp) = agent.system_prompt {
            if !sp.is_empty() {
                if let serde_json::Value::Array(ref mut arr) = body["ingredients"] {
                    // Only add if no prompt ingredient already exists
                    let has_prompt = arr.iter().any(|i| i.get("kind").and_then(|k| k.as_str()) == Some("prompt"));
                    if !has_prompt {
                        arr.push(serde_json::json!({
                            "name": "system-prompt",
                            "kind": "prompt",
                            "config": { "text": sp },
                            "required": true
                        }));
                    }
                }
            }
        }

        body
    }

    /// Serialize a Recipe into the control-plane JSON shape.
    fn recipe_to_json(&self, recipe: &Recipe) -> serde_json::Value {
        let steps: Vec<serde_json::Value> = recipe
            .steps
            .iter()
            .map(|s| {
                let kind = if s.human_gate { "human_gate" } else { s.kind.as_str() };
                let mut step = serde_json::json!({
                    "name": s.name,
                    "kind": kind,
                });
                if let Some(ref a) = s.agent {
                    step["agent_ref"] = serde_json::json!(a);
                }
                if let Some(ref t) = s.timeout {
                    step["timeout"] = serde_json::json!(t);
                }
                if !s.depends_on.is_empty() {
                    step["depends_on"] = serde_json::json!(s.depends_on);
                }
                if !s.notify.is_empty() {
                    step["notify"] = serde_json::json!(s.notify);
                }
                step
            })
            .collect();

        serde_json::json!({
            "name": recipe.name,
            "description": recipe.description,
            "version": recipe.version,
            "steps": steps,
        })
    }
}

#[pymethods]
impl AgentOvenClient {
    #[new]
    #[pyo3(signature = (url="http://localhost:8080".to_string(), api_key=None, kitchen="default".to_string()))]
    fn new(url: String, api_key: Option<String>, kitchen: String) -> Self {
        AgentOvenClient { url, api_key, kitchen }
    }

    // ── Register ─────────────────────────────────────────────

    /// Register an agent with the control plane.
    ///
    ///     client.register(agent)
    fn register(&self, agent: &Agent) -> PyResult<String> {
        let http = reqwest::blocking::Client::new();
        let url = self.api_url("/agents");
        let body = self.agent_to_json(agent);
        let req = self.authed_request(&http, reqwest::Method::POST, &url).json(&body);
        let (status, text) = self.send(req)?;
        if (200..300).contains(&status) {
            Ok(format!("✅ Agent '{}' registered — {text}", agent.name))
        } else {
            Err(PyRuntimeError::new_err(format!("Register failed ({status}): {text}")))
        }
    }

    /// Backward-compatible alias for `register()`.
    fn register_agent(&self, agent: &Agent) -> PyResult<String> {
        self.register(agent)
    }

    // ── Provider ─────────────────────────────────────────────

    /// Register a model provider.
    ///
    ///     client.register_provider("my-openai", "openai", api_key="sk-...")
    #[pyo3(signature = (name, kind, api_key=None, endpoint=None, models=vec![]))]
    fn register_provider(
        &self,
        name: &str,
        kind: &str,
        api_key: Option<String>,
        endpoint: Option<String>,
        models: Vec<String>,
    ) -> PyResult<String> {
        let http = reqwest::blocking::Client::new();
        let url = self.api_url("/models/providers");
        let mut config = serde_json::Map::new();
        if let Some(ref key) = api_key {
            config.insert("api_key".into(), serde_json::json!(key));
        }
        let mut body = serde_json::json!({
            "name": name,
            "kind": kind,
            "models": models,
            "config": config,
        });
        if let Some(ref ep) = endpoint {
            body["endpoint"] = serde_json::json!(ep);
        }
        let req = self.authed_request(&http, reqwest::Method::POST, &url).json(&body);
        let (status, text) = self.send(req)?;
        if (200..300).contains(&status) || status == 409 {
            Ok(format!("✅ Provider '{name}' registered"))
        } else {
            Err(PyRuntimeError::new_err(format!("Provider register failed ({status}): {text}")))
        }
    }

    // ── Get Agent ────────────────────────────────────────────

    /// Get a single agent by name.
    ///
    ///     agent = client.get_agent("my-agent")
    ///     print(agent.status)
    fn get_agent(&self, name: &str) -> PyResult<Agent> {
        let http = reqwest::blocking::Client::new();
        let url = self.api_url(&format!("/agents/{name}"));
        let req = self.authed_request(&http, reqwest::Method::GET, &url);
        let (status, text) = self.send(req)?;
        if !(200..300).contains(&status) {
            return Err(PyRuntimeError::new_err(format!("Get agent failed ({status}): {text}")));
        }
        let v: serde_json::Value = serde_json::from_str(&text)
            .map_err(|e| PyRuntimeError::new_err(format!("JSON parse error: {e}")))?;
        Ok(Agent {
            name: v["name"].as_str().unwrap_or("").to_string(),
            description: v["description"].as_str().unwrap_or("").to_string(),
            framework: v["framework"].as_str().unwrap_or("custom").to_string(),
            version: v["version"].as_str().unwrap_or("0.1.0").to_string(),
            model_provider: v["model_provider"].as_str().unwrap_or("").to_string(),
            model_name: v["model_name"].as_str().unwrap_or("").to_string(),
            mode: v["mode"].as_str().unwrap_or("managed").to_string(),
            system_prompt: v.get("system_prompt").and_then(|s| s.as_str()).map(|s| s.to_string()),
            ingredients: vec![],
            status: match v["status"].as_str().unwrap_or("draft") {
                "baking" => AgentStatus::Baking,
                "ready" => AgentStatus::Ready,
                "cooled" => AgentStatus::Cooled,
                "burnt" => AgentStatus::Burnt,
                "retired" => AgentStatus::Retired,
                _ => AgentStatus::Draft,
            },
        })
    }

    // ── Recipe CRUD ──────────────────────────────────────────

    /// Create a recipe without running it.
    ///
    ///     client.create_recipe(recipe)
    fn create_recipe(&self, recipe: &Recipe) -> PyResult<String> {
        let http = reqwest::blocking::Client::new();
        let url = self.api_url("/recipes");
        let body = self.recipe_to_json(recipe);
        let req = self.authed_request(&http, reqwest::Method::POST, &url).json(&body);
        let (status, text) = self.send(req)?;
        if (200..300).contains(&status) || status == 409 {
            Ok(format!("✅ Recipe '{}' created", recipe.name))
        } else {
            Err(PyRuntimeError::new_err(format!("Recipe create failed ({status}): {text}")))
        }
    }

    /// Run a recipe by name with input parameters.
    ///
    ///     result = client.bake_recipe("my-recipe", {"query": "Hello"})
    #[pyo3(signature = (name, input=None))]
    fn bake_recipe(
        &self,
        name: &str,
        input: Option<&Bound<'_, PyAny>>,
    ) -> PyResult<String> {
        let http = reqwest::blocking::Client::new();
        let url = self.api_url(&format!("/recipes/{name}/bake"));
        let body = match input {
            Some(obj) => pyany_to_json(obj)?,
            None => serde_json::json!({}),
        };
        let req = self.authed_request(&http, reqwest::Method::POST, &url).json(&body);
        let (status, text) = self.send(req)?;
        if (200..300).contains(&status) {
            Ok(text)
        } else {
            Err(PyRuntimeError::new_err(format!("Recipe bake failed ({status}): {text}")))
        }
    }

    /// List all recipes.
    fn list_recipes(&self) -> PyResult<String> {
        let http = reqwest::blocking::Client::new();
        let url = self.api_url("/recipes");
        let req = self.authed_request(&http, reqwest::Method::GET, &url);
        let (status, text) = self.send(req)?;
        if (200..300).contains(&status) {
            Ok(text)
        } else {
            Err(PyRuntimeError::new_err(format!("List recipes failed ({status}): {text}")))
        }
    }

    /// Get runs for a recipe.
    fn get_recipe_runs(&self, name: &str) -> PyResult<String> {
        let http = reqwest::blocking::Client::new();
        let url = self.api_url(&format!("/recipes/{name}/runs"));
        let req = self.authed_request(&http, reqwest::Method::GET, &url);
        let (status, text) = self.send(req)?;
        if (200..300).contains(&status) {
            Ok(text)
        } else {
            Err(PyRuntimeError::new_err(format!("Get recipe runs failed ({status}): {text}")))
        }
    }

    /// Get a specific recipe run by ID.
    fn get_recipe_run(&self, recipe_name: &str, run_id: &str) -> PyResult<String> {
        let http = reqwest::blocking::Client::new();
        let url = self.api_url(&format!("/recipes/{recipe_name}/runs/{run_id}"));
        let req = self.authed_request(&http, reqwest::Method::GET, &url);
        let (status, text) = self.send(req)?;
        if (200..300).contains(&status) {
            Ok(text)
        } else {
            Err(PyRuntimeError::new_err(format!("Get run failed ({status}): {text}")))
        }
    }

    // ── List ─────────────────────────────────────────────────

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
                    framework: format!("{:?}", a.framework).to_lowercase(),
                    version: a.version,
                    model_provider: a.model_provider,
                    model_name: a.model_name,
                    mode: format!("{}", a.mode),
                    system_prompt: a.system_prompt,
                    ingredients: vec![],
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

    // ── Bake (polymorphic) ───────────────────────────────────

    /// Deploy an agent or run a recipe.
    ///
    ///     client.bake(agent)                      # bake an agent by object
    ///     client.bake(recipe)                     # run a recipe
    ///     client.bake("summarizer")               # bake agent by name
    ///     client.bake(agent, environment="prod")  # with environment
    #[pyo3(signature = (target, version=None, environment=None, input=None))]
    fn bake(
        &self,
        target: &Bound<'_, PyAny>,
        version: Option<String>,
        environment: Option<String>,
        input: Option<String>,
    ) -> PyResult<String> {
        let http = reqwest::blocking::Client::new();

        // Agent object → POST /agents/{name}/bake
        if let Ok(agent) = target.extract::<Agent>() {
            let url = self.api_url(&format!("/agents/{}/bake", agent.name));
            let mut body = serde_json::Map::new();
            if let Some(v) = version { body.insert("version".into(), serde_json::json!(v)); }
            if let Some(e) = environment { body.insert("environment".into(), serde_json::json!(e)); }
            let req = self.authed_request(&http, reqwest::Method::POST, &url)
                .json(&serde_json::Value::Object(body));
            let (status, text) = self.send(req)?;
            return if (200..300).contains(&status) {
                Ok(format!("🔥 Agent '{}' bake started — {text}", agent.name))
            } else {
                Err(PyRuntimeError::new_err(format!("Bake failed ({status}): {text}")))
            };
        }

        // Recipe object → POST /recipes (create) then POST /recipes/{name}/bake
        if let Ok(recipe) = target.extract::<Recipe>() {
            // Create recipe first
            let create_url = self.api_url("/recipes");
            let recipe_body = self.recipe_to_json(&recipe);
            let create_req = self.authed_request(&http, reqwest::Method::POST, &create_url)
                .json(&recipe_body);
            let (cs, ct) = self.send(create_req)?;
            if !(200..300).contains(&cs) && cs != 409 {
                return Err(PyRuntimeError::new_err(format!("Recipe create failed ({cs}): {ct}")));
            }
            // Then bake
            let bake_url = self.api_url(&format!("/recipes/{}/bake", recipe.name));
            let mut body = serde_json::Map::new();
            if let Some(inp) = input { body.insert("input".into(), serde_json::json!(inp)); }
            let req = self.authed_request(&http, reqwest::Method::POST, &bake_url)
                .json(&serde_json::Value::Object(body));
            let (status, text) = self.send(req)?;
            return if (200..300).contains(&status) {
                Ok(format!("🔥 Recipe '{}' bake started — {text}", recipe.name))
            } else {
                Err(PyRuntimeError::new_err(format!("Recipe bake failed ({status}): {text}")))
            };
        }

        // String → treat as agent name
        if let Ok(name) = target.extract::<String>() {
            let url = self.api_url(&format!("/agents/{name}/bake"));
            let mut body = serde_json::Map::new();
            if let Some(v) = version { body.insert("version".into(), serde_json::json!(v)); }
            if let Some(e) = environment { body.insert("environment".into(), serde_json::json!(e)); }
            let req = self.authed_request(&http, reqwest::Method::POST, &url)
                .json(&serde_json::Value::Object(body));
            let (status, text) = self.send(req)?;
            return if (200..300).contains(&status) {
                Ok(format!("🔥 Agent '{name}' bake started — {text}"))
            } else {
                Err(PyRuntimeError::new_err(format!("Bake failed ({status}): {text}")))
            };
        }

        Err(PyRuntimeError::new_err(
            "bake() accepts an Agent, Recipe, or agent name (str)"
        ))
    }

    // ── Cool (polymorphic) ───────────────────────────────────

    /// Pause a running agent.
    ///
    ///     client.cool(agent)
    ///     client.cool("summarizer")
    fn cool(&self, target: &Bound<'_, PyAny>) -> PyResult<String> {
        let name = if let Ok(agent) = target.extract::<Agent>() {
            agent.name
        } else if let Ok(n) = target.extract::<String>() {
            n
        } else {
            return Err(PyRuntimeError::new_err("cool() accepts an Agent or agent name (str)"));
        };

        let http = reqwest::blocking::Client::new();
        let url = self.api_url(&format!("/agents/{name}/cool"));
        let req = self.authed_request(&http, reqwest::Method::POST, &url);
        let (status, text) = self.send(req)?;
        if (200..300).contains(&status) {
            Ok(format!("❄️ Agent '{name}' cooled — {text}"))
        } else {
            Err(PyRuntimeError::new_err(format!("Cool failed ({status}): {text}")))
        }
    }

    // ── Rewarm ───────────────────────────────────────────────

    /// Rewarm a cooled agent.
    ///
    ///     client.rewarm(agent)
    ///     client.rewarm("summarizer")
    fn rewarm(&self, target: &Bound<'_, PyAny>) -> PyResult<String> {
        let name = if let Ok(agent) = target.extract::<Agent>() {
            agent.name
        } else if let Ok(n) = target.extract::<String>() {
            n
        } else {
            return Err(PyRuntimeError::new_err("rewarm() accepts an Agent or agent name (str)"));
        };

        let http = reqwest::blocking::Client::new();
        let url = self.api_url(&format!("/agents/{name}/rewarm"));
        let req = self.authed_request(&http, reqwest::Method::POST, &url);
        let (status, text) = self.send(req)?;
        if (200..300).contains(&status) {
            Ok(format!("🔥 Agent '{name}' rewarmed — {text}"))
        } else {
            Err(PyRuntimeError::new_err(format!("Rewarm failed ({status}): {text}")))
        }
    }

    // ── Delete ───────────────────────────────────────────────

    /// Delete an agent from the registry.
    ///
    ///     client.delete(agent)
    ///     client.delete("summarizer")
    fn delete(&self, target: &Bound<'_, PyAny>) -> PyResult<String> {
        let name = if let Ok(agent) = target.extract::<Agent>() {
            agent.name
        } else if let Ok(n) = target.extract::<String>() {
            n
        } else {
            return Err(PyRuntimeError::new_err("delete() accepts an Agent or agent name (str)"));
        };

        let http = reqwest::blocking::Client::new();
        let url = self.api_url(&format!("/agents/{name}"));
        let req = self.authed_request(&http, reqwest::Method::DELETE, &url);
        let (status, text) = self.send(req)?;
        if (200..300).contains(&status) {
            Ok(format!("🗑️ Agent '{name}' deleted"))
        } else {
            Err(PyRuntimeError::new_err(format!("Delete failed ({status}): {text}")))
        }
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
    m.add_class::<Step>()?;
    m.add_class::<Recipe>()?;
    m.add_class::<AgentOvenClient>()?;
    Ok(())
}
