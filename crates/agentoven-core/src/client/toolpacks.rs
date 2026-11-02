//! The tool pack API (Pro, ADR-0048): installable, governed tools for enterprise systems, and the
//! OpenAPI importer. An install supplies only secret references (`vault://name`); a pack's write
//! tools wait for a person's approval by default.

use reqwest::Method;
use serde_json::{json, Value};

use super::pro::seg;
use super::AgentOvenClient;

impl AgentOvenClient {
    /// The packs the server offers.
    pub async fn list_tool_pack_catalog(&self) -> anyhow::Result<Value> {
        self.call_json(Method::GET, "/api/v1/tool-packs/catalog", None, None)
            .await
    }

    /// The packs installed in the kitchen.
    pub async fn list_tool_packs(&self) -> anyhow::Result<Value> {
        self.call_json(Method::GET, "/api/v1/tool-packs", None, None)
            .await
    }

    /// One install with its tools and their switches.
    pub async fn get_tool_pack(&self, name: &str) -> anyhow::Result<Value> {
        self.call_json(
            Method::GET,
            &format!("/api/v1/tool-packs/{}", seg(name)),
            None,
            None,
        )
        .await
    }

    /// Install a catalog pack. `body` is `{pack, secrets, name?, settings?, tools?}`.
    pub async fn install_tool_pack(&self, body: &Value) -> anyhow::Result<Value> {
        self.call_json(Method::POST, "/api/v1/tool-packs", Some(body), None)
            .await
    }

    /// Move an install to the catalog's current version.
    pub async fn upgrade_tool_pack(&self, name: &str, body: &Value) -> anyhow::Result<Value> {
        self.call_json(
            Method::POST,
            &format!("/api/v1/tool-packs/{}/upgrade", seg(name)),
            Some(body),
            None,
        )
        .await
    }

    /// Remove an install, its tools and its skill.
    pub async fn uninstall_tool_pack(&self, name: &str) -> anyhow::Result<Value> {
        self.call_json(
            Method::DELETE,
            &format!("/api/v1/tool-packs/{}", seg(name)),
            None,
            None,
        )
        .await
    }

    /// A connectivity check where one is possible.
    pub async fn check_tool_pack(&self, name: &str) -> anyhow::Result<Value> {
        self.call_json(
            Method::POST,
            &format!("/api/v1/tool-packs/{}/check", seg(name)),
            Some(&json!({})),
            None,
        )
        .await
    }

    /// Switch a tool on or off, or its approval on or off.
    pub async fn set_tool_pack_tool(
        &self,
        pack: &str,
        tool: &str,
        body: &Value,
    ) -> anyhow::Result<Value> {
        self.call_json(
            Method::PATCH,
            &format!("/api/v1/tool-packs/{}/tools/{}", seg(pack), seg(tool)),
            Some(body),
            None,
        )
        .await
    }

    /// List an OpenAPI document's operations, classified read or write, without importing.
    pub async fn preview_openapi(&self, body: &Value) -> anyhow::Result<Value> {
        self.call_json(
            Method::POST,
            "/api/v1/tool-packs/openapi/preview",
            Some(body),
            None,
        )
        .await
    }

    /// Import an OpenAPI document as a pack of governed tools.
    pub async fn import_openapi(&self, body: &Value) -> anyhow::Result<Value> {
        self.call_json(
            Method::POST,
            "/api/v1/tool-packs/openapi/import",
            Some(body),
            None,
        )
        .await
    }

    /// Hosts the kitchen allowlisted for tool packs.
    pub async fn list_egress_hosts(&self) -> anyhow::Result<Value> {
        self.call_json(Method::GET, "/api/v1/tool-packs/egress", None, None)
            .await
    }

    /// Allowlist a host for the kitchen's packs.
    pub async fn allow_egress_host(&self, host: &str) -> anyhow::Result<Value> {
        self.call_json(
            Method::POST,
            "/api/v1/tool-packs/egress",
            Some(&json!({ "host": host })),
            None,
        )
        .await
    }

    /// Remove a host from the allowlist.
    pub async fn remove_egress_host(&self, host: &str) -> anyhow::Result<Value> {
        self.call_json(
            Method::DELETE,
            &format!("/api/v1/tool-packs/egress/{}", seg(host)),
            None,
            None,
        )
        .await
    }
}
