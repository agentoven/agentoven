//! AgentOven Client — connects to the AgentOven control plane.
//!
//! Provides a high-level API for agent registration, deployment, and management.

use reqwest::Client;
use url::Url;

use crate::agent::Agent;
use crate::recipe::Recipe;

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

    /// Create from environment variables.
    ///
    /// Reads `AGENTOVEN_URL`, `AGENTOVEN_API_KEY`, `AGENTOVEN_KITCHEN`.
    pub fn from_env() -> anyhow::Result<Self> {
        let base_url =
            std::env::var("AGENTOVEN_URL").unwrap_or_else(|_| "http://localhost:8080".into());
        let api_key = std::env::var("AGENTOVEN_API_KEY").ok();
        let kitchen_id = std::env::var("AGENTOVEN_KITCHEN").ok();

        let mut client = Self::new(&base_url)?;
        client.api_key = api_key;
        client.kitchen_id = kitchen_id;
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

    /// Register a new agent in the oven (menu).
    pub async fn register(&self, agent: &Agent) -> anyhow::Result<Agent> {
        let url = self.url("/api/v1/agents");
        let resp = self
            .authed_request(self.http.post(url))
            .json(agent)
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

    /// Create a new recipe (workflow).
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
        let resp = self
            .authed_request(self.http.post(url))
            .json(&input)
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

    /// List version history for an agent.
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
        let url = self.url(&format!(
            "/api/v1/recipes/{recipe}/runs/{run_id}/gates/{step_name}/approve"
        ));
        let mut body = serde_json::json!({ "approved": approved });
        if let Some(c) = comment {
            body["comment"] = serde_json::json!(c);
        }
        let resp = self
            .authed_request(self.http.post(url))
            .json(&body)
            .send()
            .await?
            .error_for_status()?;
        Ok(resp.json().await?)
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
            b = b.header("X-Kitchen-Id", kitchen.as_str());
        }
        b
    }
}
