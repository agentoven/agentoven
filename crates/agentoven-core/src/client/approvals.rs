//! Approvals where people work (Pro ADR-0050): which decisions are waiting, who was told through which channel,
//! when they were reminded and escalated, and the non-binding replies (claim, snooze, hand on). None of these
//! decides anything; a decision is signed (see the signing calls).

use reqwest::Method;
use serde_json::{json, Value};

use super::pro::seg;
use super::AgentOvenClient;

impl AgentOvenClient {
    /// The kitchen's approval requests: `status` is `open`, `closed` or `all`; `gate` and `record_id` narrow them.
    pub async fn list_approval_requests(
        &self,
        status: Option<&str>,
        gate: Option<&str>,
        record_id: Option<&str>,
    ) -> anyhow::Result<Value> {
        let mut query: Vec<String> = Vec::new();
        for (k, v) in [("status", status), ("gate", gate), ("record_id", record_id)] {
            if let Some(v) = v.filter(|v| !v.is_empty()) {
                query.push(format!("{k}={}", seg(v)));
            }
        }
        let qs = if query.is_empty() {
            String::new()
        } else {
            format!("?{}", query.join("&"))
        };
        self.call_json(
            Method::GET,
            &format!("/api/v1/approval-requests{qs}"),
            None,
            None,
        )
        .await
    }

    /// One request with its timeline (what was sent to whom, reminders, escalations, claims) and, while it is
    /// open, the chain of people who will be asked.
    pub async fn get_approval_request(&self, id: &str) -> anyhow::Result<Value> {
        self.call_json(
            Method::GET,
            &format!("/api/v1/approval-requests/{}", seg(id)),
            None,
            None,
        )
        .await
    }

    /// The latest request for a decision (a gate and the record it reads), with its timeline, or `None`
    /// when no request was ever made for it.
    pub async fn approval_notify_status(
        &self,
        gate: &str,
        record_id: &str,
    ) -> anyhow::Result<Option<Value>> {
        let list = self
            .list_approval_requests(Some("all"), Some(gate), Some(record_id))
            .await?;
        let Some(id) = list["requests"][0]["id"].as_str() else {
            return Ok(None);
        };
        Ok(Some(self.get_approval_request(id).await?))
    }

    /// Say "I am looking at it": reminders are quiet for a while.
    pub async fn claim_approval_request(&self, id: &str) -> anyhow::Result<Value> {
        self.act_on_approval_request(id, "claim", None).await
    }

    /// Let go of a claim.
    pub async fn unclaim_approval_request(&self, id: &str) -> anyhow::Result<Value> {
        self.act_on_approval_request(id, "unclaim", None).await
    }

    /// Say "this is not mine": the request goes to the next person up at once.
    pub async fn decline_approval_request(&self, id: &str) -> anyhow::Result<Value> {
        self.act_on_approval_request(id, "decline", None).await
    }

    /// Quiet reminders and escalation for `minutes` (at most a day). The deadline does not move.
    pub async fn snooze_approval_request(&self, id: &str, minutes: u32) -> anyhow::Result<Value> {
        self.act_on_approval_request(id, "snooze", Some(json!({ "minutes": minutes })))
            .await
    }

    async fn act_on_approval_request(
        &self,
        id: &str,
        action: &str,
        body: Option<Value>,
    ) -> anyhow::Result<Value> {
        self.call_json(
            Method::POST,
            &format!("/api/v1/approval-requests/{}/{action}", seg(id)),
            body.as_ref(),
            None,
        )
        .await
    }

    /// How the kitchen chases what waits for a person: the rules by gate, the built-in default and the limits.
    pub async fn get_approval_escalation(&self) -> anyhow::Result<Value> {
        self.call_json(Method::GET, "/api/v1/approval-escalation", None, None)
            .await
    }

    /// Replace the kitchen's rules (a Head Chef's; send every rule).
    pub async fn set_approval_escalation(&self, policy: &Value) -> anyhow::Result<Value> {
        self.call_json(
            Method::PUT,
            "/api/v1/approval-escalation",
            Some(&json!({ "policy": policy })),
            None,
        )
        .await
    }
}
