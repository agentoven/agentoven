//! The secrets API (Pro, ADR-0035): secret providers, secrets, bindings, rotation and migration.
//!
//! A secret's value is written and never read back: no method here returns one. A decision (approving
//! or revoking a binding, running a migration) is signed like every human decision (ADR-0041): give a
//! [`Signature`].

use reqwest::Method;
use serde_json::{json, Value};

use super::pro::{seg, Signature};
use super::AgentOvenClient;

impl AgentOvenClient {
    /// The built-in provider and the configured ones, with what each can do and whether it is healthy.
    pub async fn list_secret_providers(&self) -> anyhow::Result<Value> {
        self.call_json(Method::GET, "/api/v1/secret-providers", None, None)
            .await
    }

    /// Add a secret provider. `config` can only narrow the platform's settings for the kind.
    pub async fn create_secret_provider(
        &self,
        name: &str,
        kind: &str,
        config: &Value,
        is_default: bool,
        platform: bool,
    ) -> anyhow::Result<Value> {
        let body = json!({"name": name, "kind": kind, "config": config, "is_default": is_default, "platform": platform});
        self.call_json(Method::POST, "/api/v1/secret-providers", Some(&body), None)
            .await
    }

    /// Make a provider the default for new secrets (`dbvault` clears the default).
    pub async fn set_default_secret_provider(
        &self,
        name: &str,
        platform: bool,
    ) -> anyhow::Result<Value> {
        let q = if platform { "?platform=true" } else { "" };
        self.call_json(
            Method::PUT,
            &format!("/api/v1/secret-providers/{}/default{q}", seg(name)),
            None,
            None,
        )
        .await
    }

    /// Remove a provider no secret lives in.
    pub async fn delete_secret_provider(
        &self,
        name: &str,
        platform: bool,
    ) -> anyhow::Result<Value> {
        let q = if platform { "?platform=true" } else { "" };
        self.call_json(
            Method::DELETE,
            &format!("/api/v1/secret-providers/{}{q}", seg(name)),
            None,
            None,
        )
        .await
    }

    /// The kitchen's secrets: metadata, never values.
    pub async fn list_secrets(&self) -> anyhow::Result<Value> {
        self.call_json(Method::GET, "/api/v1/secrets", None, None)
            .await
    }

    /// One secret's metadata.
    pub async fn get_secret(&self, name: &str) -> anyhow::Result<Value> {
        self.call_json(
            Method::GET,
            &format!("/api/v1/secrets/{}", seg(name)),
            None,
            None,
        )
        .await
    }

    /// Create a secret. `body` is `{name, provider?, value? | ref?, label?, owner?, allowed_hosts?,
    /// expires_at?, require_binding?, grace_seconds?}`: a value is written to the provider, a `ref`
    /// with no value links a secret somebody else owns.
    pub async fn create_secret(&self, body: &Value) -> anyhow::Result<Value> {
        self.call_json(Method::POST, "/api/v1/secrets", Some(body), None)
            .await
    }

    /// Change a secret's metadata (`label`, `owner`, `allowed_hosts`, `expires_at`, `clear_expiry`,
    /// `require_binding`, `grace_seconds`).
    pub async fn update_secret(&self, name: &str, body: &Value) -> anyhow::Result<Value> {
        self.call_json(
            Method::PUT,
            &format!("/api/v1/secrets/{}", seg(name)),
            Some(body),
            None,
        )
        .await
    }

    /// Delete a secret (and its value, unless it is linked).
    pub async fn delete_secret(&self, name: &str) -> anyhow::Result<Value> {
        self.call_json(
            Method::DELETE,
            &format!("/api/v1/secrets/{}", seg(name)),
            None,
            None,
        )
        .await
    }

    /// Rotate a secret to a new value. `probe` (`{url, method?, header_scheme?, expect_status?}`) makes the
    /// control plane check the new value against an upstream first; a value it rejects is not rotated in.
    pub async fn rotate_secret(
        &self,
        name: &str,
        value: &str,
        probe: Option<&Value>,
        grace_seconds: Option<u64>,
    ) -> anyhow::Result<Value> {
        let mut body = json!({"value": value});
        if let Some(p) = probe {
            body["probe"] = p.clone();
        }
        if let Some(g) = grace_seconds {
            body["grace_seconds"] = json!(g);
        }
        self.call_json(
            Method::PUT,
            &format!("/api/v1/secrets/{}/value", seg(name)),
            Some(&body),
            None,
        )
        .await
    }

    /// Pick up a rotation the secret's owner made (a linked secret).
    pub async fn refresh_secret(&self, name: &str) -> anyhow::Result<Value> {
        self.call_json(
            Method::POST,
            &format!("/api/v1/secrets/{}/refresh", seg(name)),
            None,
            None,
        )
        .await
    }

    /// A secret's versions, without values.
    pub async fn secret_versions(&self, name: &str) -> anyhow::Result<Value> {
        self.call_json(
            Method::GET,
            &format!("/api/v1/secrets/{}/versions", seg(name)),
            None,
            None,
        )
        .await
    }

    /// Bindings in the kitchen, optionally of one secret.
    pub async fn list_secret_bindings(&self, secret: Option<&str>) -> anyhow::Result<Value> {
        let q = secret
            .map(|s| format!("?secret={}", seg(s)))
            .unwrap_or_default();
        self.call_json(
            Method::GET,
            &format!("/api/v1/secret-bindings{q}"),
            None,
            None,
        )
        .await
    }

    /// Propose that `secret` be sent for `skill` to `server` (a host; empty means any host the secret
    /// allows; `-` as the skill means a tool registered by hand). Nothing happens until a person approves.
    pub async fn propose_secret_binding(
        &self,
        secret: &str,
        skill: &str,
        server: &str,
        header_scheme: &str,
    ) -> anyhow::Result<Value> {
        let body = json!({"skill": skill, "server": server, "header_scheme": header_scheme});
        self.call_json(
            Method::POST,
            &format!("/api/v1/secrets/{}/bindings", seg(secret)),
            Some(&body),
            None,
        )
        .await
    }

    /// Approve (`approved`) or revoke/reject (`rejected`) a binding: a signed decision at the
    /// `secret_binding` gate.
    pub async fn decide_secret_binding(
        &self,
        id: &str,
        meaning: &str,
        sig: Signature<'_>,
    ) -> anyhow::Result<Value> {
        let token = match sig {
            Signature::None => None,
            Signature::Token(t) => Some(t.to_string()),
            Signature::Password(p) => {
                Some(self.sign("secret_binding", id, meaning, None, p).await?)
            }
        };
        self.call_json(
            Method::POST,
            &format!("/api/v1/secret-bindings/{}/decision", seg(id)),
            Some(&json!({"meaning": meaning})),
            token.as_deref(),
        )
        .await
    }

    /// Withdraw a binding that is still pending.
    pub async fn withdraw_secret_binding(&self, id: &str) -> anyhow::Result<Value> {
        self.call_json(
            Method::DELETE,
            &format!("/api/v1/secret-bindings/{}", seg(id)),
            None,
            None,
        )
        .await
    }

    /// What moving the kitchen's secrets from one provider to another would do (nothing is changed).
    pub async fn plan_secret_migration(&self, from: &str, to: &str) -> anyhow::Result<Value> {
        let body = json!({"from": from, "to": to, "dry_run": true});
        self.call_json(Method::POST, "/api/v1/secrets/migrate", Some(&body), None)
            .await
    }

    /// Move the kitchen's secrets from one provider to another: copied, read back and compared, then
    /// repointed in one step; the source is untouched. A signed decision at the `secret_migrate` gate,
    /// bound to the digest of the plan (record `<from>:<to>`).
    pub async fn migrate_secrets(
        &self,
        from: &str,
        to: &str,
        sig: Signature<'_>,
    ) -> anyhow::Result<Value> {
        let token = match sig {
            Signature::None => None,
            Signature::Token(t) => Some(t.to_string()),
            Signature::Password(p) => Some(
                self.sign(
                    "secret_migrate",
                    &format!("{from}:{to}"),
                    "approved",
                    None,
                    p,
                )
                .await?,
            ),
        };
        let body = json!({"from": from, "to": to});
        self.call_json(
            Method::POST,
            "/api/v1/secrets/migrate",
            Some(&body),
            token.as_deref(),
        )
        .await
    }
}
