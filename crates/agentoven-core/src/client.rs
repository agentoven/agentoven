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
        let base_url = std::env::var("AGENTOVEN_URL")
            .unwrap_or_else(|_| "http://localhost:8080".into());
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
        let resp = self.authed_request(self.http.post(url))
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
        let resp = self.authed_request(self.http.get(url))
            .send()
            .await?
            .error_for_status()?;
        Ok(resp.json().await?)
    }

    /// List all agents in the current kitchen.
    pub async fn list_agents(&self) -> anyhow::Result<Vec<Agent>> {
        let url = self.url("/api/v1/agents");
        let resp = self.authed_request(self.http.get(url))
            .send()
            .await?
            .error_for_status()?;
        Ok(resp.json().await?)
    }

    /// Bake (deploy) an agent to an environment.
    pub async fn bake(&self, agent: &Agent, environment: &str) -> anyhow::Result<Agent> {
        let url = self.url(&format!(
            "/api/v1/agents/{}/bake",
            agent.name
        ));
        let resp = self.authed_request(self.http.post(url))
            .json(&serde_json::json!({
                "version": agent.version,
                "environment": environment,
            }))
            .send()
            .await?
            .error_for_status()?;
        Ok(resp.json().await?)
    }

    // ── Recipe Operations ────────────────────────────────────

    /// Create a new recipe (workflow).
    pub async fn create_recipe(&self, recipe: &Recipe) -> anyhow::Result<Recipe> {
        let url = self.url("/api/v1/recipes");
        let resp = self.authed_request(self.http.post(url))
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
        let resp = self.authed_request(self.http.post(url))
            .json(&input)
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
