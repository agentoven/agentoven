//! Inbound triggers (Pro, ADR-0047): start a recipe or an agent from an event that arrives from outside.
//!
//! A trigger runs as a service account that belongs to a person. It can start nothing the account could
//! not start itself, and it cannot skip an approval. A webhook trigger is signed by its sender (`X-AgentOven-Signature`,
//! `X-AgentOven-Timestamp`, `X-AgentOven-Delivery`; the Python and TypeScript SDKs have `sign_webhook`). Secrets in a trigger's settings are references (`vault://...`), never values.

use reqwest::Method;
use serde_json::{json, Value};

use super::pro::{seg, with_query};
use super::AgentOvenClient;

/// What can start a trigger.
pub const TRIGGER_KINDS: [&str; 3] = ["webhook", "email", "file"];
/// What a delivery's result can be.
pub const TRIGGER_DELIVERY_STATUSES: [&str; 4] = ["accepted", "rejected", "duplicate", "failed"];

/// A trigger to create. `config` holds the settings of the kind (a mailbox, a folder, a signature scheme).
#[derive(Debug, Clone, Default)]
pub struct NewTrigger<'a> {
    pub name: &'a str,
    pub service_account_id: &'a str,
    pub target: &'a str,
    /// `webhook` (the default), `email` or `file`.
    pub kind: Option<&'a str>,
    /// `recipe` (the default) or `agent`.
    pub target_kind: Option<&'a str>,
    pub environment: Option<&'a str>,
    pub description: Option<&'a str>,
    /// `$.a.b` takes a value from the payload, `text {{ $.a.b }}` puts one into text; nothing else is evaluated.
    pub input_mapping: Option<Value>,
    pub required_paths: Vec<String>,
    pub config: Option<Value>,
    pub secret_ref: Option<&'a str>,
    pub rate_limit_per_min: Option<u32>,
    /// Starts switched on unless this is `Some(false)`.
    pub enabled: Option<bool>,
}

/// Which deliveries to list.
#[derive(Debug, Clone, Default)]
pub struct DeliveryFilter<'a> {
    pub trigger_id: Option<&'a str>,
    pub status: Option<&'a str>,
    pub run_id: Option<&'a str>,
    pub limit: Option<u32>,
}

impl AgentOvenClient {
    /// The kitchen's triggers.
    pub async fn list_triggers(&self) -> anyhow::Result<Vec<Value>> {
        let v = self
            .call_json(Method::GET, "/api/v1/triggers", None, None)
            .await?;
        Ok(v["triggers"].as_array().cloned().unwrap_or_default())
    }

    pub async fn get_trigger(&self, id: &str) -> anyhow::Result<Value> {
        self.call_json(
            Method::GET,
            &format!("/api/v1/triggers/{}", seg(id)),
            None,
            None,
        )
        .await
    }

    /// Create a trigger (`trigger:manage`). A webhook trigger given no `secret_ref` gets a signing secret
    /// generated: the answer's `secret` is the only time it is shown.
    pub async fn create_trigger(&self, t: &NewTrigger<'_>) -> anyhow::Result<Value> {
        let kind = t.kind.unwrap_or("webhook");
        if !TRIGGER_KINDS.contains(&kind) {
            anyhow::bail!("kind is one of {}, not '{kind}'", TRIGGER_KINDS.join(", "));
        }
        let target_kind = t.target_kind.unwrap_or("recipe");
        if target_kind != "recipe" && target_kind != "agent" {
            anyhow::bail!("target_kind is recipe or agent, not '{target_kind}'");
        }
        let mut body = json!({
            "name": t.name, "kind": kind, "target_kind": target_kind, "target": t.target,
            "service_account_id": t.service_account_id, "enabled": t.enabled.unwrap_or(true),
            "required_paths": t.required_paths,
        });
        let obj = body.as_object_mut().expect("an object");
        if let Some(v) = t.environment {
            obj.insert("environment".into(), json!(v));
        }
        if let Some(v) = t.description {
            obj.insert("description".into(), json!(v));
        }
        if let Some(v) = &t.input_mapping {
            obj.insert("input_mapping".into(), v.clone());
        }
        if let Some(v) = &t.config {
            obj.insert("config".into(), v.clone());
        }
        if let Some(v) = t.secret_ref {
            obj.insert("secret_ref".into(), json!(v));
        }
        if let Some(v) = t.rate_limit_per_min {
            obj.insert("rate_limit_per_min".into(), json!(v));
        }
        self.call_json(Method::POST, "/api/v1/triggers", Some(&body), None)
            .await
    }

    /// Change a trigger. Switching one off needs only `trigger:manage`; anything else needs the authority
    /// of the account it runs as.
    pub async fn update_trigger(&self, id: &str, fields: &Value) -> anyhow::Result<Value> {
        self.call_json(
            Method::PUT,
            &format!("/api/v1/triggers/{}", seg(id)),
            Some(fields),
            None,
        )
        .await
    }

    pub async fn set_trigger_enabled(&self, id: &str, enabled: bool) -> anyhow::Result<Value> {
        self.update_trigger(id, &json!({ "enabled": enabled }))
            .await
    }

    pub async fn delete_trigger(&self, id: &str) -> anyhow::Result<Value> {
        self.call_json(
            Method::DELETE,
            &format!("/api/v1/triggers/{}", seg(id)),
            None,
            None,
        )
        .await
    }

    /// Replace the signing secret AgentOven generated. The answer's `secret` is the new one, shown once;
    /// the old one stops working at once.
    pub async fn rotate_trigger_secret(&self, id: &str) -> anyhow::Result<Value> {
        self.call_json(
            Method::POST,
            &format!("/api/v1/triggers/{}/rotate-secret", seg(id)),
            Some(&json!({})),
            None,
        )
        .await
    }

    /// Try a trigger with a sample payload. A dry run shows the input the run would get and starts
    /// nothing; otherwise (`trigger:fire`) a real run starts as the trigger's account, recorded as a test.
    pub async fn fire_trigger(
        &self,
        id: &str,
        payload: &Value,
        dry_run: bool,
    ) -> anyhow::Result<Value> {
        let body = json!({ "payload": payload, "dry_run": dry_run });
        self.call_json(
            Method::POST,
            &format!("/api/v1/triggers/{}/fire", seg(id)),
            Some(&body),
            None,
        )
        .await
    }

    /// Look at an e-mail or file trigger's source now (`trigger:fire`).
    pub async fn poll_trigger(&self, id: &str) -> anyhow::Result<Value> {
        self.call_json(
            Method::POST,
            &format!("/api/v1/triggers/{}/poll", seg(id)),
            Some(&json!({})),
            None,
        )
        .await
    }

    /// What happened to deliveries, newest first.
    pub async fn list_trigger_deliveries(
        &self,
        f: &DeliveryFilter<'_>,
    ) -> anyhow::Result<Vec<Value>> {
        if let Some(s) = f.status {
            if !TRIGGER_DELIVERY_STATUSES.contains(&s) {
                anyhow::bail!(
                    "status is one of {}, not '{s}'",
                    TRIGGER_DELIVERY_STATUSES.join(", ")
                );
            }
        }
        let path = with_query(
            "/api/v1/triggers/deliveries",
            &[
                ("trigger_id", f.trigger_id.map(Into::into)),
                ("status", f.status.map(Into::into)),
                ("run_id", f.run_id.map(Into::into)),
                ("limit", f.limit.map(|l| l.to_string())),
            ],
        );
        let v = self.call_json(Method::GET, &path, None, None).await?;
        Ok(v["deliveries"].as_array().cloned().unwrap_or_default())
    }
}

#[cfg(test)]
mod tests {
    use super::*;
    use crate::testing::{fake_control_plane, json as reply, Routes};

    #[tokio::test]
    async fn triggers_are_created_listed_tried_and_removed() {
        let (base, log) = fake_control_plane(Routes::from([
            (
                "POST /api/v1/triggers".to_string(),
                reply(201, json!({"trigger": {"id": "t1"}, "secret": "whsec_x"})),
            ),
            (
                "GET /api/v1/triggers".to_string(),
                reply(200, json!({"triggers": [{"id": "t1"}], "count": 1})),
            ),
            (
                "POST /api/v1/triggers/t1/fire".to_string(),
                reply(200, json!({"status": "dry_run"})),
            ),
            (
                "POST /api/v1/triggers/t1/poll".to_string(),
                reply(200, json!({"handled": 1})),
            ),
            (
                "PUT /api/v1/triggers/t1".to_string(),
                reply(200, json!({"id": "t1"})),
            ),
            (
                "DELETE /api/v1/triggers/t1".to_string(),
                reply(204, json!({})),
            ),
            (
                "GET /api/v1/triggers/deliveries?trigger_id=t1&status=failed&limit=5".to_string(),
                reply(200, json!({"deliveries": [{"id": "d1"}]})),
            ),
        ]))
        .await;
        let c = AgentOvenClient::new(&base).unwrap();
        let out = c
            .create_trigger(&NewTrigger {
                name: "inv",
                service_account_id: "sa1",
                target: "flow",
                environment: Some("staging"),
                input_mapping: Some(json!({"id": "$.invoice.id"})),
                required_paths: vec!["$.invoice.id".into()],
                rate_limit_per_min: Some(30),
                ..Default::default()
            })
            .await
            .unwrap();
        assert_eq!(out["secret"], "whsec_x");
        assert_eq!(
            log.last().body,
            json!({"name": "inv", "kind": "webhook", "target_kind": "recipe", "target": "flow", "service_account_id": "sa1",
                   "enabled": true, "required_paths": ["$.invoice.id"], "environment": "staging",
                   "input_mapping": {"id": "$.invoice.id"}, "rate_limit_per_min": 30})
        );
        assert_eq!(c.list_triggers().await.unwrap(), vec![json!({"id": "t1"})]);
        c.fire_trigger("t1", &json!({"a": 1}), true).await.unwrap();
        assert_eq!(
            log.last().body,
            json!({"payload": {"a": 1}, "dry_run": true})
        );
        assert_eq!(c.poll_trigger("t1").await.unwrap()["handled"], 1);
        c.set_trigger_enabled("t1", false).await.unwrap();
        assert_eq!(log.last().body, json!({"enabled": false}));
        c.delete_trigger("t1").await.unwrap();
        let ds = c
            .list_trigger_deliveries(&DeliveryFilter {
                trigger_id: Some("t1"),
                status: Some("failed"),
                limit: Some(5),
                ..Default::default()
            })
            .await
            .unwrap();
        assert_eq!(ds.len(), 1);
    }

    #[tokio::test]
    async fn what_the_server_would_refuse_is_refused_before_sending() {
        let (base, log) = fake_control_plane(Routes::new()).await;
        let c = AgentOvenClient::new(&base).unwrap();
        let bad = |kind, tk| NewTrigger {
            name: "x",
            service_account_id: "s",
            target: "t",
            kind,
            target_kind: tk,
            ..Default::default()
        };
        assert!(c
            .create_trigger(&bad(Some("pigeon"), None))
            .await
            .unwrap_err()
            .to_string()
            .contains("webhook, email, file"));
        assert!(c
            .create_trigger(&bad(None, Some("tool")))
            .await
            .unwrap_err()
            .to_string()
            .contains("recipe or agent"));
        let f = DeliveryFilter {
            status: Some("bogus"),
            ..Default::default()
        };
        assert!(c
            .list_trigger_deliveries(&f)
            .await
            .unwrap_err()
            .to_string()
            .contains("accepted, rejected"));
        assert!(log.all().is_empty());
    }
}
