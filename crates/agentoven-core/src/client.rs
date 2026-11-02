//! AgentOven Client — connects to the AgentOven control plane.
//!
//! Provides a high-level API for agent registration, deployment, and management.

use reqwest::Client;
use url::Url;

use crate::agent::{Agent, AgentMode};
use crate::recipe::Recipe;

/// Pro surfaces: replay, governed datasets, signed decisions, bindings and owners.
pub mod pro;
pub mod secrets;

/// Client for interacting with the AgentOven control plane.
#[derive(Debug, Clone)]
pub struct AgentOvenClient {
    /// Control plane base URL.
    base_url: Url,

    /// HTTP client.
    http: Client,

    /// API key for authentication.
    api_key: Option<String>,

    /// Current kitchen (workspace) context.
    kitchen_id: Option<String>,
}

impl AgentOvenClient {
    /// Create a new client connected to the control plane.
    pub fn new(base_url: &str) -> anyhow::Result<Self> {
        Ok(Self {
            base_url: Url::parse(base_url)?,
            http: Client::new(),
            api_key: None,
            kitchen_id: None,
        })
    }

    /// Create from config file + environment variables.
    ///
    /// Priority: CLI flags > env vars > `~/.agentoven/config.toml` > defaults.
    pub fn from_env() -> anyhow::Result<Self> {
        let cfg = crate::config::AgentOvenConfig::load();
        Self::from_config(&cfg)
    }

    /// Create from an explicit config struct (no env-var lookup).
    pub fn from_config(cfg: &crate::config::AgentOvenConfig) -> anyhow::Result<Self> {
        let mut client = Self::new(&cfg.url)?;
        client.api_key = cfg.auth_credential().map(|s| s.to_string());
        client.kitchen_id = cfg.kitchen.clone();
        Ok(client)
    }

    /// Set API key.
    pub fn with_api_key(mut self, key: impl Into<String>) -> Self {
        self.api_key = Some(key.into());
        self
    }

    /// Set the active kitchen (workspace).
    pub fn with_kitchen(mut self, kitchen_id: impl Into<String>) -> Self {
        self.kitchen_id = Some(kitchen_id.into());
        self
    }

    // ── Agent Operations ─────────────────────────────────────

    /// Register an agent in the oven (menu): create it when the kitchen has no agent of that
    /// name, update it when it does.
    ///
    /// The control plane creates with `POST /agents` and updates with `PUT /agents/{name}`
    /// (404 for a name that does not exist). A `POST` for a name that exists fails on the
    /// database store and silently replaces the agent on the in-memory one, so this asks
    /// first (`GET /agents/{name}`) and sends the one call that fits. An update changes the
    /// fields sent and leaves the others as they are.
    ///
    /// Sends only the user-provided fields; server-managed fields
    /// (id, status, kitchen, timestamps, a2a_endpoint) are set server-side.
    pub async fn register(&self, agent: &Agent) -> anyhow::Result<Agent> {
        let item_url = self.url(&format!("/api/v1/agents/{}", agent.name));
        let probe = self
            .authed_request(self.http.get(item_url.clone()))
            .send()
            .await?;
        let exists = match probe.status() {
            s if s.is_success() => true,
            reqwest::StatusCode::NOT_FOUND => false,
            other => anyhow::bail!("could not look up agent '{}' ({other})", agent.name),
        };
        let url = self.url("/api/v1/agents");

        // Build a minimal registration payload — only fields the server expects.
        let mut body = serde_json::json!({
            "name": agent.name,
            "description": agent.description,
            "framework": agent.framework,
        });
        let obj = body.as_object_mut().unwrap();

        if !agent.version.is_empty() {
            obj.insert("version".into(), agent.version.clone().into());
        }
        if !agent.model_provider.is_empty() {
            obj.insert("model_provider".into(), agent.model_provider.clone().into());
        }
        if !agent.model_name.is_empty() {
            obj.insert("model_name".into(), agent.model_name.clone().into());
        }
        if let Some(ref bp) = agent.backup_provider {
            obj.insert("backup_provider".into(), bp.clone().into());
        }
        if let Some(ref bm) = agent.backup_model {
            obj.insert("backup_model".into(), bm.clone().into());
        }
        if let Some(ref sp) = agent.system_prompt {
            obj.insert("system_prompt".into(), sp.clone().into());
        }
        if let Some(mt) = agent.max_turns {
            obj.insert("max_turns".into(), mt.into());
        }
        if !agent.skills.is_empty() {
            obj.insert("skills".into(), serde_json::to_value(&agent.skills)?);
        }
        if !agent.ingredients.is_empty() {
            obj.insert(
                "ingredients".into(),
                serde_json::to_value(&agent.ingredients)?,
            );
        }
        if !agent.tags.is_empty() {
            obj.insert("tags".into(), serde_json::to_value(&agent.tags)?);
        }
        if !agent.guardrails.is_empty() {
            obj.insert(
                "guardrails".into(),
                serde_json::to_value(&agent.guardrails)?,
            );
        }
        if agent.mode != AgentMode::default() {
            obj.insert("mode".into(), serde_json::to_value(&agent.mode)?);
        }

        let resp = if exists {
            self.authed_request(self.http.put(item_url))
        } else {
            self.authed_request(self.http.post(url))
        }
        .json(&body)
        .send()
        .await?
        .error_for_status()?;
        Ok(resp.json().await?)
    }

    /// Get an agent by name and optional version.
    pub async fn get_agent(&self, name: &str, version: Option<&str>) -> anyhow::Result<Agent> {
        let path = match version {
            Some(v) => format!("/api/v1/agents/{name}/versions/{v}"),
            None => format!("/api/v1/agents/{name}"),
        };
        let url = self.url(&path);
        let resp = self
            .authed_request(self.http.get(url))
            .send()
            .await?
            .error_for_status()?;
        Ok(resp.json().await?)
    }

    /// List all agents in the current kitchen.
    pub async fn list_agents(&self) -> anyhow::Result<Vec<Agent>> {
        let url = self.url("/api/v1/agents");
        let resp = self
            .authed_request(self.http.get(url))
            .send()
            .await?
            .error_for_status()?;
        Ok(resp.json().await?)
    }

    /// Bake (deploy) an agent to an environment.
    pub async fn bake(&self, agent: &Agent, environment: &str) -> anyhow::Result<Agent> {
        let url = self.url(&format!("/api/v1/agents/{}/bake", agent.name));
        let resp = self
            .authed_request(self.http.post(url))
            .json(&serde_json::json!({
                "version": agent.version,
                "environment": environment,
            }))
            .send()
            .await?
            .error_for_status()?;
        Ok(resp.json().await?)
    }

    /// Rewarm a cooled agent (transition back to ready).
    pub async fn rewarm(&self, name: &str) -> anyhow::Result<serde_json::Value> {
        let url = self.url(&format!("/api/v1/agents/{name}/rewarm"));
        let resp = self
            .authed_request(self.http.post(url))
            .send()
            .await?
            .error_for_status()?;
        Ok(resp.json().await?)
    }

    // ── Recipe Operations ────────────────────────────────────

    /// Create a recipe from steps already in the shape the control plane runs (see
    /// [`crate::recipe::steps_from_documents`]). Returns the created recipe as the server sent it.
    pub async fn create_recipe_steps(
        &self,
        name: &str,
        description: &str,
        steps: Vec<serde_json::Value>,
    ) -> anyhow::Result<serde_json::Value> {
        let url = self.url("/api/v1/recipes");
        let body = serde_json::json!({"name": name, "description": description, "steps": steps});
        let resp = self
            .authed_request(self.http.post(url))
            .json(&body)
            .send()
            .await?;
        let status = resp.status();
        let text = resp.text().await?;
        if !status.is_success() {
            anyhow::bail!("{status}: {text}");
        }
        Ok(serde_json::from_str(&text).unwrap_or(serde_json::Value::Null))
    }

    /// Create a new recipe (workflow) from the typed model. The typed [`Step`](crate::recipe::Step) does not
    /// carry every setting the engine reads (conditions, branches, retries, a timeout in seconds): prefer
    /// [`Self::create_recipe_steps`].
    pub async fn create_recipe(&self, recipe: &Recipe) -> anyhow::Result<Recipe> {
        let url = self.url("/api/v1/recipes");
        let resp = self
            .authed_request(self.http.post(url))
            .json(recipe)
            .send()
            .await?
            .error_for_status()?;
        Ok(resp.json().await?)
    }

    /// Bake (execute) a recipe with input.
    pub async fn bake_recipe(
        &self,
        recipe_name: &str,
        input: serde_json::Value,
    ) -> anyhow::Result<serde_json::Value> {
        let url = self.url(&format!("/api/v1/recipes/{recipe_name}/bake"));
        let body = serde_json::json!({"input": input});
        let resp = self
            .authed_request(self.http.post(url))
            .json(&body)
            .send()
            .await?
            .error_for_status()?;
        Ok(resp.json().await?)
    }

    // ── Agent Lifecycle ──────────────────────────────────────

    /// Update an existing agent.
    pub async fn update_agent(
        &self,
        name: &str,
        updates: serde_json::Value,
    ) -> anyhow::Result<Agent> {
        let url = self.url(&format!("/api/v1/agents/{name}"));
        let resp = self
            .authed_request(self.http.put(url))
            .json(&updates)
            .send()
            .await?
            .error_for_status()?;
        Ok(resp.json().await?)
    }

    /// Delete an agent.
    pub async fn delete_agent(&self, name: &str) -> anyhow::Result<()> {
        let url = self.url(&format!("/api/v1/agents/{name}"));
        self.authed_request(self.http.delete(url))
            .send()
            .await?
            .error_for_status()?;
        Ok(())
    }

    /// Re-cook an agent with edits.
    pub async fn recook_agent(
        &self,
        name: &str,
        edits: serde_json::Value,
    ) -> anyhow::Result<serde_json::Value> {
        let url = self.url(&format!("/api/v1/agents/{name}/recook"));
        let resp = self
            .authed_request(self.http.post(url))
            .json(&edits)
            .send()
            .await?
            .error_for_status()?;
        Ok(resp.json().await?)
    }

    /// Cool (pause) an agent.
    pub async fn cool_agent(&self, name: &str) -> anyhow::Result<serde_json::Value> {
        let url = self.url(&format!("/api/v1/agents/{name}/cool"));
        let resp = self
            .authed_request(self.http.post(url))
            .send()
            .await?
            .error_for_status()?;
        Ok(resp.json().await?)
    }

    /// Retire an agent permanently.
    pub async fn retire_agent(&self, name: &str) -> anyhow::Result<serde_json::Value> {
        let url = self.url(&format!("/api/v1/agents/{name}/retire"));
        let resp = self
            .authed_request(self.http.post(url))
            .send()
            .await?
            .error_for_status()?;
        Ok(resp.json().await?)
    }

    /// Test an agent (one-shot, via /test endpoint).
    pub async fn test_agent(
        &self,
        name: &str,
        message: &str,
        thinking: bool,
    ) -> anyhow::Result<serde_json::Value> {
        let url = self.url(&format!("/api/v1/agents/{name}/test"));
        let resp = self
            .authed_request(self.http.post(url))
            .json(&serde_json::json!({
                "message": message,
                "thinking_enabled": thinking,
            }))
            .send()
            .await?
            .error_for_status()?;
        Ok(resp.json().await?)
    }

    /// Invoke a managed agent (full agentic loop with execution trace).
    pub async fn invoke_agent(
        &self,
        name: &str,
        message: &str,
        variables: Option<serde_json::Value>,
        thinking: bool,
    ) -> anyhow::Result<serde_json::Value> {
        let url = self.url(&format!("/api/v1/agents/{name}/invoke"));
        let mut body = serde_json::json!({
            "message": message,
            "thinking_enabled": thinking,
        });
        if let Some(vars) = variables {
            body["variables"] = vars;
        }
        let resp = self
            .authed_request(self.http.post(url))
            .json(&body)
            .send()
            .await?
            .error_for_status()?;
        Ok(resp.json().await?)
    }

    /// Get the resolved configuration for a baked agent.
    pub async fn agent_config(&self, name: &str) -> anyhow::Result<serde_json::Value> {
        let url = self.url(&format!("/api/v1/agents/{name}/config"));
        let resp = self
            .authed_request(self.http.get(url))
            .send()
            .await?
            .error_for_status()?;
        Ok(resp.json().await?)
    }

    /// Get the A2A Agent Card for an agent.
    pub async fn agent_card(&self, name: &str) -> anyhow::Result<serde_json::Value> {
        let url = self.url(&format!("/api/v1/agents/{name}/card"));
        let resp = self
            .authed_request(self.http.get(url))
            .send()
            .await?
            .error_for_status()?;
        Ok(resp.json().await?)
    }

    /// List version history for an agent: the versions it has been baked at.
    ///
    /// A version moves only on a bake whose configuration differs from the last bake's. The first
    /// bake keeps the registered version (`0.1.0`), a bake that changes nothing keeps its version,
    /// and a changed one takes the next minor version and keeps the old one. Editing a registered
    /// agent does not bump it. [`Self::agent_version`] reads one.
    pub async fn agent_versions(&self, name: &str) -> anyhow::Result<Vec<serde_json::Value>> {
        let url = self.url(&format!("/api/v1/agents/{name}/versions"));
        let resp = self
            .authed_request(self.http.get(url))
            .send()
            .await?
            .error_for_status()?;
        Ok(resp.json().await?)
    }

    // ── Provider Operations ──────────────────────────────────

    /// List all model providers.
    pub async fn list_providers(&self) -> anyhow::Result<Vec<serde_json::Value>> {
        let url = self.url("/api/v1/models/providers");
        let resp = self
            .authed_request(self.http.get(url))
            .send()
            .await?
            .error_for_status()?;
        Ok(resp.json().await?)
    }

    /// Add a new model provider.
    pub async fn add_provider(
        &self,
        provider: serde_json::Value,
    ) -> anyhow::Result<serde_json::Value> {
        let url = self.url("/api/v1/models/providers");
        let resp = self
            .authed_request(self.http.post(url))
            .json(&provider)
            .send()
            .await?
            .error_for_status()?;
        Ok(resp.json().await?)
    }

    /// Get a specific model provider.
    pub async fn get_provider(&self, name: &str) -> anyhow::Result<serde_json::Value> {
        let url = self.url(&format!("/api/v1/models/providers/{name}"));
        let resp = self
            .authed_request(self.http.get(url))
            .send()
            .await?
            .error_for_status()?;
        Ok(resp.json().await?)
    }

    /// Update a model provider.
    pub async fn update_provider(
        &self,
        name: &str,
        provider: serde_json::Value,
    ) -> anyhow::Result<serde_json::Value> {
        let url = self.url(&format!("/api/v1/models/providers/{name}"));
        let resp = self
            .authed_request(self.http.put(url))
            .json(&provider)
            .send()
            .await?
            .error_for_status()?;
        Ok(resp.json().await?)
    }

    /// Delete a model provider.
    pub async fn delete_provider(&self, name: &str) -> anyhow::Result<()> {
        let url = self.url(&format!("/api/v1/models/providers/{name}"));
        self.authed_request(self.http.delete(url))
            .send()
            .await?
            .error_for_status()?;
        Ok(())
    }

    /// Test a provider's connectivity and credentials.
    pub async fn test_provider(&self, name: &str) -> anyhow::Result<serde_json::Value> {
        let url = self.url(&format!("/api/v1/models/providers/{name}/test"));
        let resp = self
            .authed_request(self.http.post(url))
            .send()
            .await?
            .error_for_status()?;
        Ok(resp.json().await?)
    }

    /// Discover models available from a provider.
    pub async fn discover_provider(&self, name: &str) -> anyhow::Result<serde_json::Value> {
        let url = self.url(&format!("/api/v1/models/providers/{name}/discover"));
        let resp = self
            .authed_request(self.http.post(url))
            .send()
            .await?
            .error_for_status()?;
        Ok(resp.json().await?)
    }

    // ── Tool Operations ──────────────────────────────────────

    /// List all MCP tools.
    pub async fn list_tools(&self) -> anyhow::Result<Vec<serde_json::Value>> {
        let url = self.url("/api/v1/tools");
        let resp = self
            .authed_request(self.http.get(url))
            .send()
            .await?
            .error_for_status()?;
        Ok(resp.json().await?)
    }

    /// Add a new MCP tool.
    pub async fn add_tool(&self, tool: serde_json::Value) -> anyhow::Result<serde_json::Value> {
        let url = self.url("/api/v1/tools");
        let resp = self
            .authed_request(self.http.post(url))
            .json(&tool)
            .send()
            .await?
            .error_for_status()?;
        Ok(resp.json().await?)
    }

    /// Bulk-add multiple MCP tools in one request.
    pub async fn bulk_add_tools(
        &self,
        payload: serde_json::Value,
    ) -> anyhow::Result<serde_json::Value> {
        let url = self.url("/api/v1/tools/bulk");
        let resp = self
            .authed_request(self.http.post(url))
            .json(&payload)
            .send()
            .await?
            .error_for_status()?;
        Ok(resp.json().await?)
    }

    /// Get a specific tool.
    pub async fn get_tool(&self, name: &str) -> anyhow::Result<serde_json::Value> {
        let url = self.url(&format!("/api/v1/tools/{name}"));
        let resp = self
            .authed_request(self.http.get(url))
            .send()
            .await?
            .error_for_status()?;
        Ok(resp.json().await?)
    }

    /// Update a tool.
    pub async fn update_tool(
        &self,
        name: &str,
        tool: serde_json::Value,
    ) -> anyhow::Result<serde_json::Value> {
        let url = self.url(&format!("/api/v1/tools/{name}"));
        let resp = self
            .authed_request(self.http.put(url))
            .json(&tool)
            .send()
            .await?
            .error_for_status()?;
        Ok(resp.json().await?)
    }

    /// Delete a tool.
    pub async fn delete_tool(&self, name: &str) -> anyhow::Result<()> {
        let url = self.url(&format!("/api/v1/tools/{name}"));
        self.authed_request(self.http.delete(url))
            .send()
            .await?
            .error_for_status()?;
        Ok(())
    }

    // ── Prompt Operations ────────────────────────────────────

    /// List all prompt templates.
    pub async fn list_prompts(&self) -> anyhow::Result<Vec<serde_json::Value>> {
        let url = self.url("/api/v1/prompts");
        let resp = self
            .authed_request(self.http.get(url))
            .send()
            .await?
            .error_for_status()?;
        Ok(resp.json().await?)
    }

    /// Add a new prompt template.
    pub async fn add_prompt(&self, prompt: serde_json::Value) -> anyhow::Result<serde_json::Value> {
        let url = self.url("/api/v1/prompts");
        let resp = self
            .authed_request(self.http.post(url))
            .json(&prompt)
            .send()
            .await?
            .error_for_status()?;
        Ok(resp.json().await?)
    }

    /// Get a specific prompt template.
    pub async fn get_prompt(&self, name: &str) -> anyhow::Result<serde_json::Value> {
        let url = self.url(&format!("/api/v1/prompts/{name}"));
        let resp = self
            .authed_request(self.http.get(url))
            .send()
            .await?
            .error_for_status()?;
        Ok(resp.json().await?)
    }

    /// Update a prompt template.
    pub async fn update_prompt(
        &self,
        name: &str,
        prompt: serde_json::Value,
    ) -> anyhow::Result<serde_json::Value> {
        let url = self.url(&format!("/api/v1/prompts/{name}"));
        let resp = self
            .authed_request(self.http.put(url))
            .json(&prompt)
            .send()
            .await?
            .error_for_status()?;
        Ok(resp.json().await?)
    }

    /// Delete a prompt template.
    pub async fn delete_prompt(&self, name: &str) -> anyhow::Result<()> {
        let url = self.url(&format!("/api/v1/prompts/{name}"));
        self.authed_request(self.http.delete(url))
            .send()
            .await?
            .error_for_status()?;
        Ok(())
    }

    /// Validate a prompt template.
    pub async fn validate_prompt(&self, name: &str) -> anyhow::Result<serde_json::Value> {
        let url = self.url(&format!("/api/v1/prompts/{name}/validate"));
        let resp = self
            .authed_request(self.http.post(url))
            .send()
            .await?
            .error_for_status()?;
        Ok(resp.json().await?)
    }

    /// List version history for a prompt.
    pub async fn prompt_versions(&self, name: &str) -> anyhow::Result<Vec<serde_json::Value>> {
        let url = self.url(&format!("/api/v1/prompts/{name}/versions"));
        let resp = self
            .authed_request(self.http.get(url))
            .send()
            .await?
            .error_for_status()?;
        Ok(resp.json().await?)
    }

    // ── Kitchen Operations ───────────────────────────────────

    /// List all kitchens.
    pub async fn list_kitchens(&self) -> anyhow::Result<Vec<serde_json::Value>> {
        let url = self.url("/api/v1/kitchens");
        let resp = self
            .authed_request(self.http.get(url))
            .send()
            .await?
            .error_for_status()?;
        Ok(resp.json().await?)
    }

    /// Get a specific kitchen.
    pub async fn get_kitchen(&self, id: &str) -> anyhow::Result<serde_json::Value> {
        let url = self.url(&format!("/api/v1/kitchens/{id}"));
        let resp = self
            .authed_request(self.http.get(url))
            .send()
            .await?
            .error_for_status()?;
        Ok(resp.json().await?)
    }

    /// Create a new kitchen.
    pub async fn create_kitchen(
        &self,
        kitchen: serde_json::Value,
    ) -> anyhow::Result<serde_json::Value> {
        let url = self.url("/api/v1/kitchens");
        let resp = self
            .authed_request(self.http.post(url))
            .json(&kitchen)
            .send()
            .await?
            .error_for_status()?;
        Ok(resp.json().await?)
    }

    /// Delete a kitchen.
    pub async fn delete_kitchen(&self, id: &str) -> anyhow::Result<()> {
        let url = self.url(&format!("/api/v1/kitchens/{id}"));
        self.authed_request(self.http.delete(url))
            .send()
            .await?
            .error_for_status()?;
        Ok(())
    }

    /// Get server info (edition, features, limits).
    /// The CLI calls this to discover what commands are available.
    pub async fn server_info(&self) -> anyhow::Result<serde_json::Value> {
        let url = self.url("/api/v1/info");
        let resp = self
            .authed_request(self.http.get(url))
            .send()
            .await?
            .error_for_status()?;
        Ok(resp.json().await?)
    }

    /// Get kitchen settings.
    pub async fn get_settings(&self) -> anyhow::Result<serde_json::Value> {
        let url = self.url("/api/v1/settings");
        let resp = self
            .authed_request(self.http.get(url))
            .send()
            .await?
            .error_for_status()?;
        Ok(resp.json().await?)
    }

    /// Update kitchen settings.
    pub async fn update_settings(
        &self,
        settings: serde_json::Value,
    ) -> anyhow::Result<serde_json::Value> {
        let url = self.url("/api/v1/settings");
        let resp = self
            .authed_request(self.http.put(url))
            .json(&settings)
            .send()
            .await?
            .error_for_status()?;
        Ok(resp.json().await?)
    }

    // ── Session Operations ───────────────────────────────────

    /// List sessions for an agent.
    pub async fn list_sessions(&self, agent_name: &str) -> anyhow::Result<Vec<serde_json::Value>> {
        let url = self.url(&format!("/api/v1/agents/{agent_name}/sessions"));
        let resp = self
            .authed_request(self.http.get(url))
            .send()
            .await?
            .error_for_status()?;
        Ok(resp.json().await?)
    }

    /// Create a new session for an agent.
    pub async fn create_session(&self, agent_name: &str) -> anyhow::Result<serde_json::Value> {
        let url = self.url(&format!("/api/v1/agents/{agent_name}/sessions"));
        let resp = self
            .authed_request(self.http.post(url))
            .json(&serde_json::json!({}))
            .send()
            .await?
            .error_for_status()?;
        Ok(resp.json().await?)
    }

    /// Get a specific session.
    pub async fn get_session(
        &self,
        agent_name: &str,
        session_id: &str,
    ) -> anyhow::Result<serde_json::Value> {
        let url = self.url(&format!(
            "/api/v1/agents/{agent_name}/sessions/{session_id}"
        ));
        let resp = self
            .authed_request(self.http.get(url))
            .send()
            .await?
            .error_for_status()?;
        Ok(resp.json().await?)
    }

    /// Delete a session.
    pub async fn delete_session(&self, agent_name: &str, session_id: &str) -> anyhow::Result<()> {
        let url = self.url(&format!(
            "/api/v1/agents/{agent_name}/sessions/{session_id}"
        ));
        self.authed_request(self.http.delete(url))
            .send()
            .await?
            .error_for_status()?;
        Ok(())
    }

    /// Send a message within a session.
    pub async fn send_session_message(
        &self,
        agent_name: &str,
        session_id: &str,
        message: &str,
        thinking: bool,
    ) -> anyhow::Result<serde_json::Value> {
        let url = self.url(&format!(
            "/api/v1/agents/{agent_name}/sessions/{session_id}/messages"
        ));
        let resp = self
            .authed_request(self.http.post(url))
            .json(&serde_json::json!({
                "message": message,
                "thinking_enabled": thinking,
            }))
            .send()
            .await?
            .error_for_status()?;
        Ok(resp.json().await?)
    }

    // ── RAG Operations ───────────────────────────────────────

    /// Query the RAG pipeline.
    pub async fn rag_query(&self, query: serde_json::Value) -> anyhow::Result<serde_json::Value> {
        let url = self.url("/api/v1/rag/query");
        let resp = self
            .authed_request(self.http.post(url))
            .json(&query)
            .send()
            .await?
            .error_for_status()?;
        Ok(resp.json().await?)
    }

    /// Ingest documents into the RAG pipeline.
    pub async fn rag_ingest(
        &self,
        request: serde_json::Value,
    ) -> anyhow::Result<serde_json::Value> {
        let url = self.url("/api/v1/rag/ingest");
        let resp = self
            .authed_request(self.http.post(url))
            .json(&request)
            .send()
            .await?
            .error_for_status()?;
        Ok(resp.json().await?)
    }

    // ── Trace Operations ─────────────────────────────────────

    /// List recent traces.
    pub async fn list_traces(
        &self,
        agent: Option<&str>,
        limit: u32,
    ) -> anyhow::Result<Vec<serde_json::Value>> {
        let mut url = self.url("/api/v1/traces");
        {
            let mut query = url.query_pairs_mut();
            query.append_pair("limit", &limit.to_string());
            if let Some(a) = agent {
                query.append_pair("agent", a);
            }
        }
        let resp = self
            .authed_request(self.http.get(url))
            .send()
            .await?
            .error_for_status()?;
        Ok(resp.json().await?)
    }

    /// List traces with any of the list endpoint's filters (`environment`, `agent_version`, `status`,
    /// `view` ...), newest first. Pairs with an empty value are left out.
    pub async fn list_traces_filtered(
        &self,
        filters: &[(&str, &str)],
    ) -> anyhow::Result<Vec<serde_json::Value>> {
        let mut url = self.url("/api/v1/traces");
        {
            let mut query = url.query_pairs_mut();
            for (k, v) in filters {
                if !v.is_empty() {
                    query.append_pair(k, v);
                }
            }
        }
        let resp = self
            .authed_request(self.http.get(url))
            .send()
            .await?
            .error_for_status()?;
        Ok(resp.json().await?)
    }

    /// A trace laid out on a clock, computed by the server (spans with depth, offset and duration, the
    /// critical path, retries, roll-ups by model, governance events). `cursor` is the previous page's.
    pub async fn trace_timeline(
        &self,
        trace_id: &str,
        limit: Option<u32>,
        cursor: Option<&str>,
    ) -> anyhow::Result<serde_json::Value> {
        let mut url = self.url(&format!("/api/v1/traces/{trace_id}/timeline"));
        {
            let mut query = url.query_pairs_mut();
            if let Some(l) = limit {
                query.append_pair("limit", &l.to_string());
            }
            if let Some(c) = cursor {
                query.append_pair("cursor", c);
            }
        }
        let resp = self
            .authed_request(self.http.get(url))
            .send()
            .await?
            .error_for_status()?;
        Ok(resp.json().await?)
    }

    /// The audit events recorded for a trace and the traces under it, in order, with a coverage note.
    pub async fn trace_audit(&self, trace_id: &str) -> anyhow::Result<serde_json::Value> {
        let url = self.url(&format!("/api/v1/traces/{trace_id}/audit"));
        let resp = self
            .authed_request(self.http.get(url))
            .send()
            .await?
            .error_for_status()?;
        Ok(resp.json().await?)
    }

    /// Saved views of the trace list: your own and the kitchen's shared ones.
    pub async fn list_trace_views(&self) -> anyhow::Result<Vec<serde_json::Value>> {
        let url = self.url("/api/v1/trace-views");
        let resp = self
            .authed_request(self.http.get(url))
            .send()
            .await?
            .error_for_status()?;
        Ok(resp.json().await?)
    }

    /// Save the given filters of the trace list under a name.
    pub async fn save_trace_view(
        &self,
        name: &str,
        filter: serde_json::Value,
        shared: bool,
    ) -> anyhow::Result<serde_json::Value> {
        let url = self.url("/api/v1/trace-views");
        let resp = self
            .authed_request(self.http.post(url))
            .json(&serde_json::json!({ "name": name, "filter": filter, "shared": shared }))
            .send()
            .await?
            .error_for_status()?;
        Ok(resp.json().await?)
    }

    /// Delete a saved view.
    pub async fn delete_trace_view(&self, view_id: &str) -> anyhow::Result<()> {
        let url = self.url(&format!("/api/v1/trace-views/{view_id}"));
        self.authed_request(self.http.delete(url))
            .send()
            .await?
            .error_for_status()?;
        Ok(())
    }

    /// Get a specific trace.
    pub async fn get_trace(&self, trace_id: &str) -> anyhow::Result<serde_json::Value> {
        let url = self.url(&format!("/api/v1/traces/{trace_id}"));
        let resp = self
            .authed_request(self.http.get(url))
            .send()
            .await?
            .error_for_status()?;
        Ok(resp.json().await?)
    }

    // ── Model Catalog Operations ─────────────────────────────

    /// List model catalog.
    pub async fn model_catalog(&self) -> anyhow::Result<Vec<serde_json::Value>> {
        let url = self.url("/api/v1/models/catalog");
        let resp = self
            .authed_request(self.http.get(url))
            .send()
            .await?
            .error_for_status()?;
        Ok(resp.json().await?)
    }

    /// Refresh model catalog from providers.
    pub async fn catalog_refresh(&self) -> anyhow::Result<serde_json::Value> {
        let url = self.url("/api/v1/models/catalog/refresh");
        let resp = self
            .authed_request(self.http.post(url))
            .send()
            .await?
            .error_for_status()?;
        Ok(resp.json().await?)
    }

    /// Get cost summary.
    pub async fn model_cost(&self) -> anyhow::Result<serde_json::Value> {
        let url = self.url("/api/v1/models/cost");
        let resp = self
            .authed_request(self.http.get(url))
            .send()
            .await?
            .error_for_status()?;
        Ok(resp.json().await?)
    }

    // ── Recipe Extended Operations ───────────────────────────

    /// Get a specific recipe.
    pub async fn get_recipe(&self, name: &str) -> anyhow::Result<serde_json::Value> {
        let url = self.url(&format!("/api/v1/recipes/{name}"));
        let resp = self
            .authed_request(self.http.get(url))
            .send()
            .await?
            .error_for_status()?;
        Ok(resp.json().await?)
    }

    /// List all recipes.
    pub async fn list_recipes(&self) -> anyhow::Result<Vec<serde_json::Value>> {
        let url = self.url("/api/v1/recipes");
        let resp = self
            .authed_request(self.http.get(url))
            .send()
            .await?
            .error_for_status()?;
        Ok(resp.json().await?)
    }

    /// Delete a recipe.
    pub async fn delete_recipe(&self, name: &str) -> anyhow::Result<()> {
        let url = self.url(&format!("/api/v1/recipes/{name}"));
        self.authed_request(self.http.delete(url))
            .send()
            .await?
            .error_for_status()?;
        Ok(())
    }

    /// List runs for a recipe.
    pub async fn recipe_runs(&self, name: &str) -> anyhow::Result<Vec<serde_json::Value>> {
        let url = self.url(&format!("/api/v1/recipes/{name}/runs"));
        let resp = self
            .authed_request(self.http.get(url))
            .send()
            .await?
            .error_for_status()?;
        Ok(resp.json().await?)
    }

    /// Approve a human gate in a recipe run.
    pub async fn approve_gate(
        &self,
        recipe: &str,
        run_id: &str,
        step_name: &str,
        approved: bool,
        comment: Option<&str>,
    ) -> anyhow::Result<serde_json::Value> {
        self.approve_gate_signed(recipe, run_id, step_name, approved, comment, None)
            .await
    }

    /// Approve or reject a human gate with a signing token from [`Self::sign_decision`]. On Pro every
    /// decision is signed; without a token the decision is sent unsigned, as the community edition expects.
    pub async fn approve_gate_signed(
        &self,
        recipe: &str,
        run_id: &str,
        step_name: &str,
        approved: bool,
        comment: Option<&str>,
        signing_token: Option<&str>,
    ) -> anyhow::Result<serde_json::Value> {
        let url = self.url(&format!(
            "/api/v1/recipes/{recipe}/runs/{run_id}/gates/{step_name}/approve"
        ));
        let mut body = serde_json::json!({ "approved": approved });
        if let Some(c) = comment {
            body["comment"] = serde_json::json!(c);
        }
        let mut req = self.authed_request(self.http.post(url)).json(&body);
        if let Some(t) = signing_token {
            req = req.header("X-Signing-Token", t);
        }
        let resp = req.send().await?;
        if !resp.status().is_success() {
            let status = resp.status();
            let text = resp.text().await.unwrap_or_default();
            anyhow::bail!("{status}: {}", signing_message(&text));
        }
        Ok(resp.json().await?)
    }

    /// Prove who you are again and get the single-use signing token for one decision (Pro, ADR-0041).
    /// `gate` names the kind of decision (`recipe_gate`, `skill_approval`, ...), `record_id` what is decided,
    /// `meaning` is `approved` or `rejected`, `password` is the password of the local account signed in with.
    /// Same as [`Self::sign`] for a decision with no payload.
    pub async fn sign_decision(
        &self,
        gate: &str,
        record_id: &str,
        meaning: &str,
        password: &str,
    ) -> anyhow::Result<String> {
        self.sign(gate, record_id, meaning, None, password).await
    }

    // ── Audit Operations ─────────────────────────────────────

    /// List audit events.
    pub async fn list_audit(&self, limit: u32) -> anyhow::Result<Vec<serde_json::Value>> {
        let mut url = self.url("/api/v1/audit");
        url.query_pairs_mut()
            .append_pair("limit", &limit.to_string());
        let resp = self
            .authed_request(self.http.get(url))
            .send()
            .await?
            .error_for_status()?;
        Ok(resp.json().await?)
    }

    // ── Guardrail Operations ─────────────────────────────────

    /// List available guardrail kinds.
    pub async fn guardrail_kinds(&self) -> anyhow::Result<Vec<serde_json::Value>> {
        let url = self.url("/api/v1/guardrails/kinds");
        let resp = self
            .authed_request(self.http.get(url))
            .send()
            .await?
            .error_for_status()?;
        Ok(resp.json().await?)
    }

    // ── Generic HTTP helpers (for Pro-gated commands) ──────

    /// Generic GET that returns deserialized JSON.
    pub async fn raw_get<T: serde::de::DeserializeOwned>(&self, path: &str) -> anyhow::Result<T> {
        let url = self.url(path);
        let resp = self
            .authed_request(self.http.get(url))
            .send()
            .await?
            .error_for_status()?;
        Ok(resp.json().await?)
    }

    /// Generic POST that sends JSON and returns deserialized JSON.
    pub async fn raw_post<T: serde::de::DeserializeOwned>(
        &self,
        path: &str,
        body: &serde_json::Value,
    ) -> anyhow::Result<T> {
        let url = self.url(path);
        let resp = self
            .authed_request(self.http.post(url))
            .json(body)
            .send()
            .await?
            .error_for_status()?;
        Ok(resp.json().await?)
    }

    /// Generic PUT that sends JSON and returns deserialized JSON.
    pub async fn raw_put<T: serde::de::DeserializeOwned>(
        &self,
        path: &str,
        body: &serde_json::Value,
    ) -> anyhow::Result<T> {
        let url = self.url(path);
        let resp = self
            .authed_request(self.http.put(url))
            .json(body)
            .send()
            .await?
            .error_for_status()?;
        Ok(resp.json().await?)
    }

    /// Generic GET returning the response body verbatim.
    ///
    /// Used for endpoints that serve a stored file rather than JSON, so the
    /// bytes a caller writes to disk are the bytes the server holds.
    pub async fn raw_get_text(&self, path: &str) -> anyhow::Result<String> {
        let url = self.url(path);
        let resp = self
            .authed_request(self.http.get(url))
            .send()
            .await?
            .error_for_status()?;
        Ok(resp.text().await?)
    }

    /// Generic DELETE.
    pub async fn raw_delete(&self, path: &str) -> anyhow::Result<()> {
        let url = self.url(path);
        self.authed_request(self.http.delete(url))
            .send()
            .await?
            .error_for_status()?;
        Ok(())
    }

    // ── Internal ─────────────────────────────────────────────

    fn url(&self, path: &str) -> Url {
        self.base_url.join(path).expect("Invalid URL path")
    }

    fn authed_request(&self, builder: reqwest::RequestBuilder) -> reqwest::RequestBuilder {
        let mut b = builder;
        if let Some(ref key) = self.api_key {
            b = b.bearer_auth(key);
        }
        if let Some(ref kitchen) = self.kitchen_id {
            b = b.header("X-Kitchen", kitchen.as_str());
            b = b.header("X-Kitchen-Id", kitchen.as_str());
        }
        b
    }
}

/// The message in a signing error body (`{"error": code, "message": text}`), or the body itself.
fn signing_message(body: &str) -> String {
    serde_json::from_str::<serde_json::Value>(body)
        .ok()
        .and_then(|v| {
            let code = v["error"].as_str().unwrap_or_default().to_string();
            let msg = v["message"].as_str().unwrap_or_default().to_string();
            match (code.is_empty(), msg.is_empty()) {
                (false, false) => Some(format!("{code}: {msg}")),
                (false, true) => Some(code),
                (true, false) => Some(msg),
                _ => None,
            }
        })
        .unwrap_or_else(|| body.to_string())
}

#[cfg(test)]
mod register_tests {
    use super::*;
    use std::collections::HashMap;
    use std::sync::{Arc, Mutex};
    use tokio::io::{AsyncReadExt, AsyncWriteExt};
    use tokio::net::TcpListener;

    type Seen = Arc<Mutex<Vec<(String, String, String)>>>; // (method, path, body)

    /// A loopback HTTP server that records each request and answers from a
    /// `"METHOD /path" -> (status, json)` table (404 for anything not listed).
    async fn fake_control_plane(
        routes: HashMap<&'static str, (u16, serde_json::Value)>,
    ) -> (String, Seen) {
        let listener = TcpListener::bind("127.0.0.1:0").await.unwrap();
        let base = format!("http://{}", listener.local_addr().unwrap());
        let seen: Seen = Arc::default();
        let recorded = seen.clone();
        tokio::spawn(async move {
            loop {
                let (mut sock, _) = listener.accept().await.unwrap();
                let routes = routes.clone();
                let recorded = recorded.clone();
                tokio::spawn(async move {
                    let mut buf = Vec::new();
                    let mut chunk = [0u8; 4096];
                    let (head_end, content_len) = loop {
                        let n = sock.read(&mut chunk).await.unwrap();
                        if n == 0 {
                            return;
                        }
                        buf.extend_from_slice(&chunk[..n]);
                        if let Some(pos) = buf.windows(4).position(|w| w == b"\r\n\r\n") {
                            let head = String::from_utf8_lossy(&buf[..pos]).to_lowercase();
                            let len = head
                                .lines()
                                .find_map(|l| l.strip_prefix("content-length:"))
                                .and_then(|v| v.trim().parse::<usize>().ok())
                                .unwrap_or(0);
                            break (pos + 4, len);
                        }
                    };
                    while buf.len() < head_end + content_len {
                        let n = sock.read(&mut chunk).await.unwrap();
                        buf.extend_from_slice(&chunk[..n]);
                    }
                    let head = String::from_utf8_lossy(&buf[..head_end]).to_string();
                    let mut first = head.lines().next().unwrap().split_whitespace();
                    let (method, path) = (first.next().unwrap(), first.next().unwrap());
                    let body = String::from_utf8_lossy(&buf[head_end..head_end + content_len]).to_string();
                    recorded
                        .lock()
                        .unwrap()
                        .push((method.to_string(), path.to_string(), body));
                    let (status, json) = routes
                        .get(format!("{method} {path}").as_str())
                        .cloned()
                        .unwrap_or((404, serde_json::json!({"error": "no route"})));
                    let payload = json.to_string();
                    let reply = format!(
                        "HTTP/1.1 {status} X\r\nContent-Type: application/json\r\nContent-Length: {}\r\nConnection: close\r\n\r\n{payload}",
                        payload.len()
                    );
                    let _ = sock.write_all(reply.as_bytes()).await;
                });
            }
        });
        (base, seen)
    }

    fn agent() -> Agent {
        Agent::builder("refund-bot")
            .version("1.0.0")
            .description("Pays refunds")
            .build()
    }

    fn calls(seen: &Seen) -> Vec<(String, String)> {
        seen.lock().unwrap().iter().map(|(m, p, _)| (m.clone(), p.clone())).collect()
    }

    #[tokio::test]
    async fn register_creates_with_post_when_the_agent_does_not_exist() {
        let stored = serde_json::to_value(agent()).unwrap();
        let routes = HashMap::from([("POST /api/v1/agents", (201, stored))]);
        let (base, seen) = fake_control_plane(routes).await;

        let client = AgentOvenClient::new(&base).unwrap().with_kitchen("acme");
        let out = client.register(&agent()).await.unwrap();

        assert_eq!(out.name, "refund-bot");
        assert_eq!(
            calls(&seen),
            vec![
                ("GET".to_string(), "/api/v1/agents/refund-bot".to_string()),
                ("POST".to_string(), "/api/v1/agents".to_string()),
            ]
        );
        let body: serde_json::Value = serde_json::from_str(&seen.lock().unwrap()[1].2).unwrap();
        assert_eq!(body["name"], "refund-bot");
        assert_eq!(body["description"], "Pays refunds");
    }

    #[tokio::test]
    async fn register_updates_with_put_when_the_agent_exists() {
        let stored = serde_json::to_value(agent()).unwrap();
        let routes = HashMap::from([
            ("GET /api/v1/agents/refund-bot", (200, stored.clone())),
            ("PUT /api/v1/agents/refund-bot", (200, stored)),
        ]);
        let (base, seen) = fake_control_plane(routes).await;

        let client = AgentOvenClient::new(&base).unwrap();
        let out = client.register(&agent()).await.unwrap();

        assert_eq!(out.name, "refund-bot");
        assert_eq!(
            calls(&seen),
            vec![
                ("GET".to_string(), "/api/v1/agents/refund-bot".to_string()),
                ("PUT".to_string(), "/api/v1/agents/refund-bot".to_string()),
            ]
        );
    }

    #[tokio::test]
    async fn register_fails_loudly_when_the_lookup_fails() {
        let routes = HashMap::from([(
            "GET /api/v1/agents/refund-bot",
            (500, serde_json::json!({"error": "boom"})),
        )]);
        let (base, seen) = fake_control_plane(routes).await;

        let client = AgentOvenClient::new(&base).unwrap();
        assert!(client.register(&agent()).await.is_err());
        // It did not guess: nothing was created or changed.
        assert_eq!(calls(&seen).len(), 1);
    }
}
