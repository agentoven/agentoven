//! Guardrail rules (Pro workspace rules, ADR-0049) and the test route (`POST /api/v1/guardrails/test`, which the
//! community edition has too, for rules sent in the body).

use reqwest::Method;
use serde_json::{json, Value};

use super::pro::seg;
use super::AgentOvenClient;

impl AgentOvenClient {
    /// The kitchen's workspace guardrail rules, each with its `action`.
    pub async fn list_guardrails(&self) -> anyhow::Result<Value> {
        self.call_json(Method::GET, "/api/v1/guardrails", None, None)
            .await
    }

    /// The kinds, stages, actions and PII/secret detectors a rule can use.
    pub async fn guardrail_kinds(&self) -> anyhow::Result<Value> {
        self.call_json(Method::GET, "/api/v1/guardrails/kinds", None, None)
            .await
    }

    /// Create a rule. The server checks it and refuses one that could not work.
    pub async fn create_guardrail(&self, rule: &Value) -> anyhow::Result<Value> {
        self.call_json(Method::POST, "/api/v1/guardrails", Some(rule), None)
            .await
    }

    /// Change a rule: send only the fields to change.
    pub async fn update_guardrail(&self, id: &str, patch: &Value) -> anyhow::Result<Value> {
        self.call_json(
            Method::PUT,
            &format!("/api/v1/guardrails/{}", seg(id)),
            Some(patch),
            None,
        )
        .await
    }

    /// Flip a rule on or off.
    pub async fn toggle_guardrail(&self, id: &str) -> anyhow::Result<Value> {
        self.call_json(
            Method::PATCH,
            &format!("/api/v1/guardrails/{}/toggle", seg(id)),
            Some(&json!({})),
            None,
        )
        .await
    }

    /// Delete a rule.
    pub async fn delete_guardrail(&self, id: &str) -> anyhow::Result<Value> {
        self.call_json(
            Method::DELETE,
            &format!("/api/v1/guardrails/{}", seg(id)),
            None,
            None,
        )
        .await
    }

    /// What `rules` (or, with none, the kitchen's rules) would do to `text` at `stage`, without running an
    /// agent or storing anything. `include_custom` also calls custom endpoint rules.
    pub async fn test_guardrails(
        &self,
        text: &str,
        stage: &str,
        rules: &[Value],
        include_custom: bool,
    ) -> anyhow::Result<Value> {
        let mut body = json!({"text": text, "stage": stage});
        if !rules.is_empty() {
            body["rules"] = Value::Array(rules.to_vec());
        }
        if include_custom {
            body["include_custom"] = Value::Bool(true);
        }
        self.call_json(Method::POST, "/api/v1/guardrails/test", Some(&body), None)
            .await
    }
}
