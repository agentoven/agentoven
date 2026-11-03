//! The Pro surfaces of the control plane: replay, governed datasets, signed decisions, identity
//! bindings and credential owners, agent versions.
//!
//! Everything here is a thin, honest wrapper over one route. Bodies are the server's own JSON; a
//! refusal comes back as an [`ApiError`] carrying the HTTP status and the server's `error` code and
//! `message`, so a caller can tell `signature_required` from `legal_only`.
//!
//! A decision at a gate is signed (ADR-0041): the person proves who they are again and gets a
//! single-use token bound to exactly that decision, sent in the `X-Signing-Token` header.
//! [`AgentOvenClient::sign`] does both steps. A method that takes a decision takes a [`Signature`]:
//! a password (the method signs it for you), a token you already hold, or none.

use reqwest::Method;
use serde_json::{json, Value};

use super::AgentOvenClient;

/// The header that carries a signing token on a decision call (the control plane's `signatures.Header`).
pub const SIGNING_TOKEN_HEADER: &str = "X-Signing-Token";

/// The roles a person or credential can hold inside one kitchen. `restaurateur` is platform-wide, so
/// it is a role of a person in the user directory, never of a kitchen member, binding or credential.
/// The retired `admin` is no role: it was a Head Chef inside a kitchen and a Restaurateur across the
/// installation.
pub const KITCHEN_ROLES: [&str; 8] = [
    "head_chef",
    "chef",
    "baker",
    "taster",
    "auditor",
    "legal",
    "finance",
    "viewer",
];

/// The roles of a person in the user directory: the kitchen roles and the platform-wide `restaurateur`.
pub const PERSON_ROLES: [&str; 9] = [
    "restaurateur",
    "head_chef",
    "chef",
    "baker",
    "taster",
    "auditor",
    "legal",
    "finance",
    "viewer",
];

/// The gates a decision can be signed at. `legal_hold_release` lifts a legal hold (a dataset's or the
/// audit trail's) and is signed by the legal team; `approval_skip` runs a recipe on an environment that
/// requires approval, without that approval.
pub const SIGNING_GATES: [&str; 16] = [
    "tool_approval",
    "budget_extension",
    "resume_guidance",
    "recipe_gate",
    "wait_release",
    "environment_promotion",
    "skill_approval",
    "bot_action",
    "live_tool_replay",
    "dataset_publish",
    "dataset_purge",
    "legal_hold_release",
    "approval_skip",
    "secret_binding",
    "secret_migrate",
    "budget_raise",
];

/// Retention modes of a dataset.
pub const RETENTION_MODES: [&str; 3] = ["expire", "persist", "regulated"];

/// What a replayed run's tool calls do.
pub const REPLAY_TOOL_MODES: [&str; 3] = ["frozen", "blocked", "live"];

/// Refuse a role the control plane does not have. `admin` is retired.
pub fn check_role(role: &str, allowed: &[&str], what: &str) -> anyhow::Result<()> {
    if role == "admin" {
        let person = if allowed.contains(&"restaurateur") {
            " or 'restaurateur' for a person across the installation"
        } else {
            ""
        };
        anyhow::bail!(
            "role 'admin' is retired ({what}): use 'head_chef' for a kitchen role{person}"
        );
    }
    if !allowed.contains(&role) {
        let hint = if role == "restaurateur" {
            " ('restaurateur' is platform-wide: assign it in the user directory)"
        } else {
            ""
        };
        anyhow::bail!(
            "unknown role '{role}' for {what}: one of {}{hint}",
            allowed.join(", ")
        );
    }
    Ok(())
}

/// Refuse a retention mode the control plane does not have.
pub fn check_retention_mode(mode: &str) -> anyhow::Result<()> {
    if !RETENTION_MODES.contains(&mode) {
        anyhow::bail!(
            "retention_mode is one of {}, not '{mode}'",
            RETENTION_MODES.join(", ")
        );
    }
    Ok(())
}

/// The error for a refused request: a [`crate::ConfigurationError`] (with its problems) for a 422 whose `code`
/// is `invalid_configuration`, an [`ApiError`] for everything else.
pub(crate) fn failure(
    status: u16,
    text: &str,
    headers: &reqwest::header::HeaderMap,
) -> anyhow::Error {
    let api = ApiError::from_response(status, text, headers);
    match crate::refcheck::ConfigurationError::from_reply(status, &api.detail) {
        Some(config) => config.into(),
        None => api.into(),
    }
}

/// A refusal from the control plane: the HTTP status and the server's `error` code and `message`.
///
/// A rate limit (429, `rate_limited`) carries `retry_after` seconds, and a budget that is used up (402,
/// `run_budget_exhausted`) says whose budget it was: both are in `detail`, the reply's own JSON, and
/// have accessors below. Nothing here retries for the caller.
#[derive(Debug, Clone)]
pub struct ApiError {
    pub status: u16,
    pub code: String,
    pub message: String,
    /// Seconds to wait before the same request is admitted: the `Retry-After` header, else the body's
    /// `retry_after`. Set for a rate limit.
    pub retry_after: Option<u64>,
    /// The reply's JSON (`Null` when it was not JSON).
    pub detail: Value,
}

impl ApiError {
    fn from_response(status: u16, text: &str, headers: &reqwest::header::HeaderMap) -> Self {
        let v: Value = serde_json::from_str(text).unwrap_or(Value::Null);
        let code = v["error"].as_str().unwrap_or_default().to_string();
        let message = v["message"].as_str().unwrap_or_default().to_string();
        let message = if code.is_empty() && message.is_empty() {
            text.trim().to_string()
        } else {
            message
        };
        let retry_after = headers
            .get(reqwest::header::RETRY_AFTER)
            .and_then(|h| h.to_str().ok())
            .and_then(|h| h.trim().parse::<u64>().ok())
            .or_else(|| v["retry_after"].as_u64());
        Self {
            status,
            code,
            message,
            retry_after,
            detail: v,
        }
    }

    /// A rate limit refused the request (HTTP 429, `rate_limited`).
    pub fn is_rate_limited(&self) -> bool {
        self.status == 429 && self.code == "rate_limited"
    }

    /// A budget is used up (HTTP 402, `run_budget_exhausted` or `budget_exceeded`).
    pub fn is_budget_exceeded(&self) -> bool {
        self.status == 402
            && (self.code == "run_budget_exhausted" || self.code == "budget_exceeded")
    }

    /// What is limited or whose budget it is: `kitchen`, `agent`, `api_key`, `provider` (or `run`).
    pub fn scope(&self) -> Option<&str> {
        self.detail["scope"].as_str()
    }

    /// The unit of a budget refusal: `usd` or `tokens`.
    pub fn unit(&self) -> Option<&str> {
        match self.detail["unit"].as_str() {
            Some(u) => Some(u),
            None => match self.detail["dimension"].as_str() {
                Some("cost") => Some("usd"),
                Some("tokens") => Some("tokens"),
                _ => None,
            },
        }
    }

    /// When a period budget starts again, if the reply says.
    pub fn resets_at(&self) -> Option<&str> {
        self.detail["resets_at"].as_str()
    }
}

impl std::fmt::Display for ApiError {
    fn fmt(&self, f: &mut std::fmt::Formatter<'_>) -> std::fmt::Result {
        write!(f, "HTTP {}", self.status)?;
        if !self.code.is_empty() {
            write!(f, ": {}", self.code)?;
        }
        if !self.message.is_empty() {
            write!(f, ": {}", self.message)?;
        }
        Ok(())
    }
}

impl std::error::Error for ApiError {}

/// This account signs through the identity provider (OIDC), which re-asks for the password in a
/// browser. Signing here takes a local account's password; sign in the dashboard, or use a local account.
#[derive(Debug, Clone)]
pub struct OidcSigningRequired;

impl std::fmt::Display for OidcSigningRequired {
    fn fmt(&self, f: &mut std::fmt::Formatter<'_>) -> std::fmt::Result {
        write!(
            f,
            "this account signs through the identity provider (OIDC), which needs a browser: sign this \
decision in the dashboard, or sign in with a local account (password) to sign from the command line"
        )
    }
}

impl std::error::Error for OidcSigningRequired {}

/// How a decision is signed.
#[derive(Debug, Clone, Copy, Default)]
pub enum Signature<'a> {
    /// Unsigned: the community edition, or a decision that needs no signature.
    #[default]
    None,
    /// The password of the local account signed in with: the method signs the decision for you.
    Password(&'a str),
    /// A single-use signing token you already hold, signed for exactly this decision.
    Token(&'a str),
}

/// A dataset version exported as JSONL: the manifest line, the case lines after it, the digest the
/// server sent in `X-Manifest-Digest`, and the text as received.
#[derive(Debug, Clone)]
pub struct DatasetExport {
    pub version: u64,
    pub manifest_digest: String,
    pub manifest: Value,
    pub cases: Vec<Value>,
    pub text: String,
}

/// Escape one URL path segment.
pub fn seg(value: &str) -> String {
    let mut out = String::with_capacity(value.len());
    for b in value.bytes() {
        match b {
            b'A'..=b'Z' | b'a'..=b'z' | b'0'..=b'9' | b'-' | b'.' | b'_' | b'~' => {
                out.push(b as char)
            }
            _ => out.push_str(&format!("%{b:02X}")),
        }
    }
    out
}

/// `path` with the pairs whose value is set, as a query string.
pub(super) fn with_query(path: &str, pairs: &[(&str, Option<String>)]) -> String {
    let mut ser = url::form_urlencoded::Serializer::new(String::new());
    let mut any = false;
    for (k, v) in pairs {
        if let Some(v) = v {
            if !v.is_empty() {
                ser.append_pair(k, v);
                any = true;
            }
        }
    }
    if any {
        format!("{path}?{}", ser.finish())
    } else {
        path.to_string()
    }
}

fn signing_request(
    gate: &str,
    record_id: &str,
    meaning: &str,
    payload: Option<&Value>,
) -> anyhow::Result<Value> {
    if meaning != "approved" && meaning != "rejected" {
        anyhow::bail!("meaning is 'approved' or 'rejected', not '{meaning}'");
    }
    Ok(json!({
        "gate": gate, "record_id": record_id, "meaning": meaning,
        "payload": payload.cloned().unwrap_or_else(|| json!({})),
    }))
}

impl AgentOvenClient {
    // ── The one request every method below makes ─────────────

    async fn call(
        &self,
        method: Method,
        path: &str,
        body: Option<&Value>,
        signing_token: Option<&str>,
    ) -> anyhow::Result<(String, reqwest::header::HeaderMap)> {
        let mut req = self.authed_request(self.http.request(method, self.url(path)));
        if let Some(t) = signing_token {
            req = req.header(SIGNING_TOKEN_HEADER, t);
        }
        if let Some(b) = body {
            req = req.json(b);
        }
        let resp = req.send().await?;
        let status = resp.status();
        let headers = resp.headers().clone();
        let text = resp.text().await.unwrap_or_default();
        if !status.is_success() {
            return Err(failure(status.as_u16(), &text, &headers));
        }
        Ok((text, headers))
    }

    /// Send a file as the raw request body (an upload) and return the JSON reply. A refusal is an
    /// [`ApiError`], as for [`Self::call_json`].
    pub(super) async fn call_bytes(
        &self,
        path: &str,
        data: Vec<u8>,
        content_type: &str,
    ) -> anyhow::Result<Value> {
        let resp = self
            .authed_request(self.http.request(Method::POST, self.url(path)))
            .header(reqwest::header::CONTENT_TYPE, content_type)
            .body(data)
            .send()
            .await?;
        let status = resp.status();
        let headers = resp.headers().clone();
        let text = resp.text().await.unwrap_or_default();
        if !status.is_success() {
            return Err(failure(status.as_u16(), &text, &headers));
        }
        Ok(serde_json::from_str(&text)?)
    }

    /// Send one request, optionally with a signing token in the `X-Signing-Token` header, and return the
    /// JSON reply (`{}` for an empty one). A refusal is an [`ApiError`]. This is the way to call a
    /// decision this client has no method for.
    pub async fn call_json(
        &self,
        method: Method,
        path: &str,
        body: Option<&Value>,
        signing_token: Option<&str>,
    ) -> anyhow::Result<Value> {
        let (text, _) = self.call(method, path, body, signing_token).await?;
        if text.trim().is_empty() {
            Ok(json!({}))
        } else {
            Ok(serde_json::from_str(&text)?)
        }
    }

    /// The kitchen this client sends (`X-Kitchen`), or `default`, which the server uses when none is sent.
    pub fn kitchen(&self) -> &str {
        self.kitchen_id.as_deref().unwrap_or("default")
    }

    // ── Signing ──────────────────────────────────────────────

    /// Sign one decision and return its single-use signing token: asks for the challenge, proves who
    /// you are with `password`, and returns the token (good for a minute and for exactly this decision).
    ///
    /// `gate` is the kind of decision (see [`SIGNING_GATES`]), `record_id` what is decided, `meaning` is
    /// `approved` or `rejected`, and `payload` is the content of the decision that is signed with it.
    /// Send the token in the `X-Signing-Token` header (the other methods do, given a [`Signature::Token`]).
    ///
    /// Two gates are new with the roles. `legal_hold_release` (record id `dataset:<id>` or
    /// `audit:<kitchen>`, payload `{"reason": ...}`) lifts a legal hold, and `approval_skip` (record id
    /// `<recipe>@<environment>`, payload `{"input": ...}`) runs a recipe on an environment that requires
    /// approval. [`Self::set_dataset_legal_hold`], [`Self::set_audit_legal_hold`] and [`Self::run_recipe`]
    /// sign them for you.
    ///
    /// An API key or service account cannot sign. An account that signs in through an identity provider
    /// gets [`OidcSigningRequired`] before the password is sent anywhere.
    pub async fn sign(
        &self,
        gate: &str,
        record_id: &str,
        meaning: &str,
        payload: Option<&Value>,
        password: &str,
    ) -> anyhow::Result<String> {
        let request = signing_request(gate, record_id, meaning, payload)?;
        let challenge = self.signature_challenge(&request).await?;
        if challenge["reauth"].as_str() == Some("oidc") {
            return Err(OidcSigningRequired.into());
        }
        let digest = challenge["subject"]["digest"]
            .as_str()
            .ok_or_else(|| anyhow::anyhow!("the control plane sent no digest to sign"))?;
        let mut body = request;
        body["digest"] = json!(digest);
        body["password"] = json!(password);
        let reply = match self.request_signing_token(&body).await {
            Ok(v) => v,
            Err(e) => {
                if e.downcast_ref::<ApiError>()
                    .is_some_and(|a| a.code == "use_oidc")
                {
                    return Err(OidcSigningRequired.into());
                }
                return Err(e);
            }
        };
        reply["signing_token"]
            .as_str()
            .map(String::from)
            .ok_or_else(|| anyhow::anyhow!("the control plane sent no signing token"))
    }

    /// The first half of signing: what would be signed, and how to prove who you are. `request` is
    /// `{gate, record_id, meaning, payload}`.
    pub async fn signature_challenge(&self, request: &Value) -> anyhow::Result<Value> {
        self.call_json(
            Method::POST,
            "/api/v1/signatures/challenge",
            Some(request),
            None,
        )
        .await
    }

    /// The second half of signing: `request` is the challenge's request plus `digest` and `password`.
    /// Prefer [`Self::sign`].
    pub async fn request_signing_token(&self, request: &Value) -> anyhow::Result<Value> {
        self.call_json(
            Method::POST,
            "/api/v1/signatures/token",
            Some(request),
            None,
        )
        .await
    }

    /// The token for one decision: the one passed, one signed now with the password, or none.
    pub async fn signing_token_for(
        &self,
        sig: Signature<'_>,
        gate: &str,
        record_id: &str,
        payload: Option<&Value>,
    ) -> anyhow::Result<Option<String>> {
        Ok(match sig {
            Signature::None => None,
            Signature::Token(t) => Some(t.to_string()),
            Signature::Password(p) => {
                Some(self.sign(gate, record_id, "approved", payload, p).await?)
            }
        })
    }

    /// Signed decisions of the kitchen, narrowed by `gate` and `record_id` when they are not empty.
    pub async fn list_signatures(&self, gate: &str, record_id: &str) -> anyhow::Result<Vec<Value>> {
        let path = with_query(
            "/api/v1/signatures",
            &[
                ("gate", Some(gate.into())),
                ("record_id", Some(record_id.into())),
            ],
        );
        Ok(serde_json::from_value(
            self.call_json(Method::GET, &path, None, None).await?,
        )?)
    }

    /// One signed decision.
    pub async fn get_signature(&self, id: &str) -> anyhow::Result<Value> {
        self.call_json(
            Method::GET,
            &format!("/api/v1/signatures/{}", seg(id)),
            None,
            None,
        )
        .await
    }

    /// Check the kitchen's signature chain.
    pub async fn verify_signatures(&self) -> anyhow::Result<Value> {
        self.call_json(Method::GET, "/api/v1/signatures/verify", None, None)
            .await
    }

    /// Who may approve what in this kitchen: `{policy, gates}`.
    pub async fn approver_policy(&self) -> anyhow::Result<Value> {
        self.call_json(Method::GET, "/api/v1/approver-policy", None, None)
            .await
    }

    /// Replace the kitchen's approver rules (a Head Chef's). `policy` maps `<gate>` or `<gate>:<scope>`
    /// to `{roles, users}`; every role is checked here against [`PERSON_ROLES`] (`admin` is refused).
    pub async fn set_approver_policy(&self, policy: &Value) -> anyhow::Result<Value> {
        let rules = policy.as_object().ok_or_else(|| {
            anyhow::anyhow!("the policy is an object of <gate> to {{roles, users}}")
        })?;
        let mut out = serde_json::Map::new();
        for (key, rule) in rules {
            let roles: Vec<String> = rule["roles"]
                .as_array()
                .map(|a| {
                    a.iter()
                        .filter_map(|r| r.as_str().map(String::from))
                        .collect()
                })
                .unwrap_or_default();
            for role in &roles {
                check_role(role, &PERSON_ROLES, &format!("approver policy '{key}'"))?;
            }
            let users = rule["users"].clone();
            out.insert(
                key.clone(),
                json!({ "roles": roles, "users": if users.is_array() { users } else { json!([]) } }),
            );
        }
        self.call_json(
            Method::PUT,
            "/api/v1/approver-policy",
            Some(&json!({ "policy": out })),
            None,
        )
        .await
    }

    // ── Replay ───────────────────────────────────────────────

    /// Re-run one captured model call as up to five variants (the server's limit); each is
    /// `{"label": ..., "edits": {...}}`. Runs no tool. With no variants the call is re-run as captured.
    pub async fn replay_call(
        &self,
        trace_id: &str,
        span_id: &str,
        variants: &[Value],
        reason: &str,
    ) -> anyhow::Result<Value> {
        let path = format!(
            "/api/v1/traces/{}/spans/{}/replay",
            seg(trace_id),
            seg(span_id)
        );
        self.call_json(
            Method::POST,
            &path,
            Some(&json!({ "variants": variants, "reason": reason })),
            None,
        )
        .await
    }

    /// Re-run an agent run from one model call (`from_span`) with `edits`. `tools` is `frozen` (the
    /// captured results, stopping at the first call with none), `blocked` (the call is returned, not run) or
    /// `live` (only `live_tools`, only idempotent ones, a signed decision on the `live_tool_replay` gate:
    /// pass a [`Signature`]; a password has this method ask [`Self::prepare_live_replay`] what to sign).
    #[allow(clippy::too_many_arguments)] // one argument per field of the route's body
    pub async fn replay_run(
        &self,
        trace_id: &str,
        from_span: &str,
        edits: &Value,
        tools: &str,
        live_tools: &[String],
        reason: &str,
        sig: Signature<'_>,
    ) -> anyhow::Result<Value> {
        if !REPLAY_TOOL_MODES.contains(&tools) {
            anyhow::bail!(
                "tools must be one of {}, not '{tools}'",
                REPLAY_TOOL_MODES.join(", ")
            );
        }
        if tools == "live" && live_tools.is_empty() {
            anyhow::bail!("tools \"live\" needs live_tools naming the tools that may run live");
        }
        if tools != "live" && (!live_tools.is_empty() || !matches!(sig, Signature::None)) {
            anyhow::bail!("live_tools and a signature only go with tools \"live\"");
        }
        let token = match sig {
            Signature::Password(p) => {
                let prep = self
                    .prepare_live_replay(trace_id, from_span, edits, live_tools, reason)
                    .await?;
                let gate = prep["gate"].as_str().unwrap_or("live_tool_replay");
                let record = prep["record_id"]
                    .as_str()
                    .ok_or_else(|| anyhow::anyhow!("the control plane sent no record to sign"))?;
                Some(
                    self.sign(gate, record, "approved", Some(&prep["payload"]), p)
                        .await?,
                )
            }
            Signature::Token(t) => Some(t.to_string()),
            Signature::None => None,
        };
        let body = json!({
            "from_span": from_span, "edits": edits, "tools": tools,
            "live_tools": live_tools, "reason": reason,
        });
        self.call_json(
            Method::POST,
            &format!("/api/v1/traces/{}/replay", seg(trace_id)),
            Some(&body),
            token.as_deref(),
        )
        .await
    }

    /// Check a live replay as far as it can be checked and return what to sign: `{gate, record_id,
    /// payload}`, computed by the server so a client never reproduces the digest of the edits.
    pub async fn prepare_live_replay(
        &self,
        trace_id: &str,
        from_span: &str,
        edits: &Value,
        live_tools: &[String],
        reason: &str,
    ) -> anyhow::Result<Value> {
        let body = json!({ "from_span": from_span, "edits": edits, "live_tools": live_tools, "reason": reason });
        self.call_json(
            Method::POST,
            &format!("/api/v1/traces/{}/replay/prepare", seg(trace_id)),
            Some(&body),
            None,
        )
        .await
    }

    /// A model call as it was captured, whole (`trace:read:content`; the read is audited).
    pub async fn span_capture(&self, trace_id: &str, span_id: &str) -> anyhow::Result<Value> {
        let path = format!(
            "/api/v1/traces/{}/spans/{}/capture",
            seg(trace_id),
            seg(span_id)
        );
        self.call_json(Method::GET, &path, None, None).await
    }

    // ── Governed datasets ────────────────────────────────────

    /// The kitchen's datasets.
    pub async fn list_datasets(&self) -> anyhow::Result<Vec<Value>> {
        Ok(serde_json::from_value(
            self.call_json(Method::GET, "/api/v1/datasets", None, None)
                .await?,
        )?)
    }

    /// Create a dataset. `body` is `{name, description, retention_mode, ttl_days, review_by,
    /// min_retention_days, purpose}`; a retention mode the server does not have is refused here.
    pub async fn create_dataset(&self, body: &Value) -> anyhow::Result<Value> {
        if let Some(m) = body["retention_mode"].as_str() {
            check_retention_mode(m)?;
        }
        self.call_json(Method::POST, "/api/v1/datasets", Some(body), None)
            .await
    }

    /// One dataset with its retention state.
    pub async fn get_dataset(&self, id: &str) -> anyhow::Result<Value> {
        self.call_json(
            Method::GET,
            &format!("/api/v1/datasets/{}", seg(id)),
            None,
            None,
        )
        .await
    }

    /// Change a dataset's settings. The server takes them whole: what is left out is cleared.
    pub async fn update_dataset(&self, id: &str, body: &Value) -> anyhow::Result<Value> {
        if let Some(m) = body["retention_mode"].as_str() {
            check_retention_mode(m)?;
        }
        self.call_json(
            Method::PUT,
            &format!("/api/v1/datasets/{}", seg(id)),
            Some(body),
            None,
        )
        .await
    }

    /// The cases of a dataset's draft.
    pub async fn list_dataset_cases(
        &self,
        id: &str,
        retired: bool,
        limit: Option<u32>,
        offset: Option<u32>,
    ) -> anyhow::Result<Value> {
        let path = with_query(
            &format!("/api/v1/datasets/{}/cases", seg(id)),
            &[
                ("retired", retired.then(|| "true".to_string())),
                ("limit", limit.map(|n| n.to_string())),
                ("offset", offset.map(|n| n.to_string())),
            ],
        );
        self.call_json(Method::GET, &path, None, None).await
    }

    /// Add a case: `{input, expected_output, metadata, provenance}`; `input` is required.
    pub async fn add_dataset_case(&self, id: &str, body: &Value) -> anyhow::Result<Value> {
        self.call_json(
            Method::POST,
            &format!("/api/v1/datasets/{}/cases", seg(id)),
            Some(body),
            None,
        )
        .await
    }

    /// Copy a span of a trace of this kitchen into a case: `{trace_id, span_id, expected_output?, metadata?}`.
    /// An `expected_output` that is present (even null) replaces the span's own output.
    pub async fn add_dataset_case_from_trace(
        &self,
        id: &str,
        body: &Value,
    ) -> anyhow::Result<Value> {
        self.call_json(
            Method::POST,
            &format!("/api/v1/datasets/{}/cases/from-trace", seg(id)),
            Some(body),
            None,
        )
        .await
    }

    /// Take a case out of the draft.
    pub async fn retire_dataset_case(&self, id: &str, case_id: &str) -> anyhow::Result<()> {
        let path = format!("/api/v1/datasets/{}/cases/{}", seg(id), seg(case_id));
        self.call_json(Method::DELETE, &path, None, None)
            .await
            .map(|_| ())
    }

    /// Erase a case's payload (`dataset:purge`); the content hash and the manifests stay.
    pub async fn erase_dataset_case(
        &self,
        id: &str,
        case_id: &str,
        reason: &str,
    ) -> anyhow::Result<Value> {
        let path = format!("/api/v1/datasets/{}/cases/{}/erase", seg(id), seg(case_id));
        self.call_json(
            Method::POST,
            &path,
            Some(&json!({ "reason": reason })),
            None,
        )
        .await
    }

    /// The published versions of a dataset.
    pub async fn list_dataset_versions(&self, id: &str) -> anyhow::Result<Vec<Value>> {
        let path = format!("/api/v1/datasets/{}/versions", seg(id));
        Ok(serde_json::from_value(
            self.call_json(Method::GET, &path, None, None).await?,
        )?)
    }

    /// One version with its manifest.
    pub async fn get_dataset_version(&self, id: &str, version: u64) -> anyhow::Result<Value> {
        let path = format!("/api/v1/datasets/{}/versions/{version}", seg(id));
        self.call_json(Method::GET, &path, None, None).await
    }

    /// Recompute a version: its manifest digest, the chain and the content hashes.
    pub async fn verify_dataset_version(&self, id: &str, version: u64) -> anyhow::Result<Value> {
        let path = format!("/api/v1/datasets/{}/versions/{version}/verify", seg(id));
        self.call_json(Method::GET, &path, None, None).await
    }

    /// Publish the draft as the next version. A regulated dataset's version is a signed decision on the
    /// `dataset_publish` gate (record id: the dataset's id); other datasets ignore the signature.
    pub async fn publish_dataset(&self, id: &str, sig: Signature<'_>) -> anyhow::Result<Value> {
        let token = self
            .signing_token_for(sig, "dataset_publish", id, None)
            .await?;
        self.call_json(
            Method::POST,
            &format!("/api/v1/datasets/{}/versions", seg(id)),
            Some(&json!({})),
            token.as_deref(),
        )
        .await
    }

    /// Export a version (the latest when `version` is `None`) as JSONL.
    pub async fn export_dataset(
        &self,
        id: &str,
        version: Option<u64>,
    ) -> anyhow::Result<DatasetExport> {
        let path = with_query(
            &format!("/api/v1/datasets/{}/export", seg(id)),
            &[("version", version.map(|v| v.to_string()))],
        );
        let (text, headers) = self.call(Method::GET, &path, None, None).await?;
        let mut lines = Vec::new();
        for l in text.lines().filter(|l| !l.trim().is_empty()) {
            lines.push(serde_json::from_str::<Value>(l)?);
        }
        let manifest = lines.first().cloned().unwrap_or(Value::Null);
        let manifest_digest = headers
            .get("x-manifest-digest")
            .and_then(|v| v.to_str().ok())
            .map(String::from)
            .or_else(|| manifest["manifest_digest"].as_str().map(String::from))
            .unwrap_or_default();
        Ok(DatasetExport {
            version: manifest["version"].as_u64().unwrap_or(0),
            manifest_digest,
            manifest,
            cases: lines.into_iter().skip(1).collect(),
            text,
        })
    }

    /// Lower a regulated dataset's minimum retention (`dataset:purge`): a signed decision on the
    /// `dataset_purge` gate (payload `{action: "lower_minimum", new_minimum_days, reason}`).
    pub async fn lower_dataset_minimum(
        &self,
        id: &str,
        min_retention_days: u32,
        reason: &str,
        sig: Signature<'_>,
    ) -> anyhow::Result<Value> {
        let payload = json!({ "action": "lower_minimum", "new_minimum_days": min_retention_days, "reason": reason });
        let token = self
            .signing_token_for(sig, "dataset_purge", id, Some(&payload))
            .await?;
        self.call_json(
            Method::POST,
            &format!("/api/v1/datasets/{}/retention/lower-minimum", seg(id)),
            Some(&json!({ "min_retention_days": min_retention_days, "reason": reason })),
            token.as_deref(),
        )
        .await
    }

    /// Place (`held`) or lift a legal hold on a dataset. Placing only protects and takes no signature.
    /// Lifting is the Legal role's decision on the `legal_hold_release` gate (record id `dataset:<id>`,
    /// payload `{reason}`); a signature given when placing is ignored.
    pub async fn set_dataset_legal_hold(
        &self,
        id: &str,
        held: bool,
        reason: &str,
        sig: Signature<'_>,
    ) -> anyhow::Result<Value> {
        let token = if held {
            None
        } else {
            self.signing_token_for(
                sig,
                "legal_hold_release",
                &format!("dataset:{id}"),
                Some(&json!({ "reason": reason })),
            )
            .await?
        };
        self.call_json(
            Method::PUT,
            &format!("/api/v1/datasets/{}/legal-hold", seg(id)),
            Some(&json!({ "held": held, "reason": reason })),
            token.as_deref(),
        )
        .await
    }

    /// Archive a dataset, then delete it, leaving a tombstone. A regulated dataset's purge is a signed
    /// decision on the `dataset_purge` gate (payload `{action: "purge", reason}`).
    pub async fn purge_dataset(
        &self,
        id: &str,
        reason: &str,
        sig: Signature<'_>,
    ) -> anyhow::Result<Value> {
        let payload = json!({ "action": "purge", "reason": reason });
        let token = self
            .signing_token_for(sig, "dataset_purge", id, Some(&payload))
            .await?;
        self.call_json(
            Method::POST,
            &format!("/api/v1/datasets/{}/purge", seg(id)),
            Some(&json!({ "reason": reason })),
            token.as_deref(),
        )
        .await
    }

    /// The datasets the retention janitor removes within `days` (30 by default).
    pub async fn list_expiring_datasets(&self, days: Option<u32>) -> anyhow::Result<Vec<Value>> {
        let path = with_query(
            "/api/v1/datasets/expiring",
            &[("days", days.map(|d| d.to_string()))],
        );
        Ok(serde_json::from_value(
            self.call_json(Method::GET, &path, None, None).await?,
        )?)
    }

    /// The `persist` datasets due for review within `days` (30 by default).
    pub async fn list_dataset_reviews(&self, days: Option<u32>) -> anyhow::Result<Vec<Value>> {
        let path = with_query(
            "/api/v1/datasets/reviews",
            &[("days", days.map(|d| d.to_string()))],
        );
        Ok(serde_json::from_value(
            self.call_json(Method::GET, &path, None, None).await?,
        )?)
    }

    /// What stays of purged datasets.
    pub async fn list_dataset_tombstones(&self) -> anyhow::Result<Vec<Value>> {
        Ok(serde_json::from_value(
            self.call_json(Method::GET, "/api/v1/datasets/tombstones", None, None)
                .await?,
        )?)
    }

    /// Place (`hold`) or lift the legal hold on the kitchen's audit trail. Lifting is the Legal role's
    /// decision on the `legal_hold_release` gate: record id `audit:<kitchen>` (this client's kitchen, or
    /// `default`), payload `{reason}`.
    pub async fn set_audit_legal_hold(
        &self,
        hold: bool,
        reason: &str,
        sig: Signature<'_>,
    ) -> anyhow::Result<Value> {
        let kitchen = self.kitchen();
        let token = if hold {
            None
        } else {
            self.signing_token_for(
                sig,
                "legal_hold_release",
                &format!("audit:{kitchen}"),
                Some(&json!({ "reason": reason })),
            )
            .await?
        };
        self.call_json(
            Method::POST,
            "/api/v1/audit/legal-hold",
            Some(&json!({ "hold": hold, "reason": reason })),
            token.as_deref(),
        )
        .await
    }

    // ── Recipes on an environment that requires approval ─────

    /// Start a run. An environment can require approval; a Head Chef or Restaurateur may skip it
    /// (never in a GxP kitchen), and the skip is a signed decision on the `approval_skip` gate: record id
    /// `<recipe>@<environment>`, payload `{input}`. Pass a [`Signature`] to skip; it needs `environment`.
    pub async fn run_recipe(
        &self,
        recipe: &str,
        input: &Value,
        environment: Option<&str>,
        sig: Signature<'_>,
    ) -> anyhow::Result<Value> {
        let mut body = json!({ "input": input });
        if let Some(env) = environment {
            body["environment"] = json!(env);
        }
        self.precheck_recipe_run(recipe, environment).await?;
        let token = match (sig, environment) {
            (Signature::None, _) => None,
            (_, None) => anyhow::bail!(
                "skipping an approval is signed for one environment: name the environment"
            ),
            (sig, Some(env)) => {
                self.signing_token_for(
                    sig,
                    "approval_skip",
                    &format!("{recipe}@{env}"),
                    Some(&json!({ "input": input })),
                )
                .await?
            }
        };
        self.call_json(
            Method::POST,
            &format!("/api/v1/recipes/{}/bake", seg(recipe)),
            Some(&body),
            token.as_deref(),
        )
        .await
    }

    // ── Identity-provider bindings, credential owners, my kitchens ──

    /// Which group or app role of the identity provider is which role in a kitchen.
    pub async fn list_kitchen_bindings(&self, kitchen_id: &str) -> anyhow::Result<Vec<Value>> {
        let path = format!("/api/v1/kitchens/{}/bindings", seg(kitchen_id));
        Ok(serde_json::from_value(
            self.call_json(Method::GET, &path, None, None).await?,
        )?)
    }

    /// Bind an identity-provider group (its object id) or app role value to a kitchen role (one of
    /// [`KITCHEN_ROLES`]; `restaurateur` is platform-wide and `admin` is retired).
    pub async fn create_kitchen_binding(
        &self,
        kitchen_id: &str,
        claim_value: &str,
        role: &str,
        label: &str,
    ) -> anyhow::Result<Value> {
        check_role(role, &KITCHEN_ROLES, "a kitchen binding")?;
        if claim_value.trim().is_empty() {
            anyhow::bail!("claim_value is the group object id or app role value the identity provider puts in the token");
        }
        self.call_json(
            Method::POST,
            &format!("/api/v1/kitchens/{}/bindings", seg(kitchen_id)),
            Some(&json!({ "claim_value": claim_value, "role": role, "label": label })),
            None,
        )
        .await
    }

    /// Remove a binding.
    pub async fn delete_kitchen_binding(
        &self,
        kitchen_id: &str,
        binding_id: &str,
    ) -> anyhow::Result<()> {
        let path = format!(
            "/api/v1/kitchens/{}/bindings/{}",
            seg(kitchen_id),
            seg(binding_id)
        );
        self.call_json(Method::DELETE, &path, None, None)
            .await
            .map(|_| ())
    }

    /// Give a service account to another person (a member of the kitchen at a rank that covers its role).
    pub async fn set_service_account_owner(
        &self,
        id: &str,
        owner_email: &str,
    ) -> anyhow::Result<Value> {
        self.call_json(
            Method::POST,
            &format!("/api/v1/service-accounts/{}/owner", seg(id)),
            Some(&json!({ "owner_email": owner_email })),
            None,
        )
        .await
    }

    /// Give a scoped API key to another person (same rules as a service account).
    pub async fn set_scoped_key_owner(&self, id: &str, owner_email: &str) -> anyhow::Result<Value> {
        self.call_json(
            Method::POST,
            &format!("/api/v1/keys/{}/owner", seg(id)),
            Some(&json!({ "owner_email": owner_email })),
            None,
        )
        .await
    }

    /// The kitchens the caller can enter and their role in each: `{enforced, kitchens}`.
    pub async fn my_kitchens(&self) -> anyhow::Result<Value> {
        self.call_json(Method::GET, "/api/v1/me/kitchens", None, None)
            .await
    }

    // ── Agent versions ───────────────────────────────────────
    // An agent's version moves only on a bake whose configuration differs from the last bake's: the
    // first bake keeps the registered version (0.1.0), a bake that changes nothing keeps its version,
    // and a changed one takes the next minor version and keeps the old one. Editing a registered agent
    // does not bump it.

    /// One baked version of an agent, as the agent was configured then (`0.1.0` is the first bake).
    pub async fn agent_version(&self, name: &str, version: &str) -> anyhow::Result<Value> {
        let path = format!("/api/v1/agents/{}/versions/{}", seg(name), seg(version));
        self.call_json(Method::GET, &path, None, None).await
    }
}

#[cfg(test)]
mod tests {
    use super::*;
    use crate::testing::{fake_control_plane, json as reply, Reply, Routes};

    const DIGEST: &str = "dddddddddddddddddddddddddddddddddddddddddddddddddddddddddddddddd";

    fn signing_routes() -> Routes {
        Routes::from([
            (
                "POST /api/v1/signatures/challenge".to_string(),
                reply(
                    200,
                    json!({"subject": {"digest": DIGEST}, "reauth": "password"}),
                ),
            ),
            (
                "POST /api/v1/signatures/token".to_string(),
                reply(200, json!({"signing_token": "tok.sig"})),
            ),
        ])
    }

    fn with(mut routes: Routes, extra: &[(&str, Reply)]) -> Routes {
        for (k, v) in extra {
            routes.insert(k.to_string(), v.clone());
        }
        routes
    }

    async fn client(routes: Routes) -> (AgentOvenClient, crate::testing::Log) {
        let (base, log) = fake_control_plane(routes).await;
        (
            AgentOvenClient::new(&base)
                .unwrap()
                .with_api_key("ao_key")
                .with_kitchen("acme"),
            log,
        )
    }

    #[test]
    fn the_roles_are_the_real_ones_and_admin_is_not_one() {
        assert_eq!(KITCHEN_ROLES.len(), 8);
        assert!(!PERSON_ROLES.contains(&"admin") && !KITCHEN_ROLES.contains(&"admin"));
        assert!(PERSON_ROLES.contains(&"restaurateur") && !KITCHEN_ROLES.contains(&"restaurateur"));
        assert!(
            SIGNING_GATES.contains(&"legal_hold_release")
                && SIGNING_GATES.contains(&"approval_skip")
        );
        assert_eq!(SIGNING_TOKEN_HEADER, "X-Signing-Token");
    }

    #[test]
    fn check_role_refuses_admin_with_a_hint_and_unknown_roles() {
        let e = check_role("admin", &KITCHEN_ROLES, "a service account")
            .unwrap_err()
            .to_string();
        assert!(
            e.contains("retired") && e.contains("head_chef") && !e.contains("restaurateur"),
            "{e}"
        );
        let e = check_role("admin", &PERSON_ROLES, "a user")
            .unwrap_err()
            .to_string();
        assert!(e.contains("restaurateur"), "{e}");
        let e = check_role("restaurateur", &KITCHEN_ROLES, "a binding")
            .unwrap_err()
            .to_string();
        assert!(e.contains("platform-wide"), "{e}");
        assert!(check_role("sous_chef", &KITCHEN_ROLES, "x").is_err());
        assert!(check_role("legal", &KITCHEN_ROLES, "x").is_ok());
        assert!(check_role("restaurateur", &PERSON_ROLES, "x").is_ok());
    }

    #[test]
    fn path_segments_are_escaped() {
        assert_eq!(seg("t/1 x"), "t%2F1%20x");
        assert_eq!(seg("a-b_c.d~e"), "a-b_c.d~e");
    }

    #[tokio::test]
    async fn sign_runs_the_challenge_then_the_token() {
        let (c, log) = client(signing_routes()).await;
        let token = c
            .sign(
                "legal_hold_release",
                "dataset:ds1",
                "approved",
                Some(&json!({"reason": "closed"})),
                "pw",
            )
            .await
            .unwrap();
        assert_eq!(token, "tok.sig");
        let seen = log.all();
        let want = json!({"gate": "legal_hold_release", "record_id": "dataset:ds1", "meaning": "approved", "payload": {"reason": "closed"}});
        assert_eq!(seen[0].path, "/api/v1/signatures/challenge");
        assert_eq!(seen[0].body, want);
        assert_eq!(seen[1].body["digest"], DIGEST);
        assert_eq!(seen[1].body["password"], "pw");
        assert!(seen[0].body.get("password").is_none());
        assert_eq!(seen[0].headers["authorization"], "Bearer ao_key");
        assert_eq!(seen[0].headers["x-kitchen"], "acme");
    }

    #[tokio::test]
    async fn the_older_sign_decision_is_the_same_call_with_no_payload() {
        let (c, log) = client(signing_routes()).await;
        assert_eq!(
            c.sign_decision("recipe_gate", "r1:s", "approved", "pw")
                .await
                .unwrap(),
            "tok.sig"
        );
        assert_eq!(log.all()[0].body["payload"], json!({}));
        assert!(c
            .sign("recipe_gate", "r1:s", "maybe", None, "pw")
            .await
            .is_err());
    }

    #[tokio::test]
    async fn an_oidc_account_is_told_so_and_the_password_goes_nowhere() {
        let routes = with(
            signing_routes(),
            &[(
                "POST /api/v1/signatures/challenge",
                reply(
                    200,
                    json!({"subject": {"digest": DIGEST}, "reauth": "oidc"}),
                ),
            )],
        );
        let (c, log) = client(routes).await;
        let e = c
            .sign("recipe_gate", "r1:s", "approved", None, "pw")
            .await
            .unwrap_err();
        assert!(e.downcast_ref::<OidcSigningRequired>().is_some(), "{e}");
        assert_eq!(log.calls(), vec!["POST /api/v1/signatures/challenge"]);

        let routes = with(
            signing_routes(),
            &[(
                "POST /api/v1/signatures/token",
                reply(400, json!({"error": "use_oidc", "message": "x"})),
            )],
        );
        let (c, _) = client(routes).await;
        let e = c
            .sign("recipe_gate", "r1:s", "approved", None, "pw")
            .await
            .unwrap_err();
        assert!(e.downcast_ref::<OidcSigningRequired>().is_some(), "{e}");
    }

    #[tokio::test]
    async fn a_refusal_keeps_its_status_and_code() {
        let routes = Routes::from([(
            "POST /api/v1/signatures/challenge".to_string(),
            reply(
                403,
                json!({"error": "signature_required", "message": "only a person can sign"}),
            ),
        )]);
        let (c, _) = client(routes).await;
        let e = c
            .sign("recipe_gate", "r1:s", "approved", None, "pw")
            .await
            .unwrap_err();
        let api = e.downcast_ref::<ApiError>().expect("an ApiError");
        assert_eq!((api.status, api.code.as_str()), (403, "signature_required"));
        assert!(e.to_string().contains("only a person can sign"), "{e}");
    }

    #[tokio::test]
    async fn replay_a_call_sends_the_variants_and_reason() {
        let routes = Routes::from([(
            "POST /api/v1/traces/t%2F1/spans/s1/replay".to_string(),
            reply(200, json!({"variants": [{"status": "completed"}]})),
        )]);
        let (c, log) = client(routes).await;
        let v = json!({"label": "cooler", "edits": {"temperature": 0.1}});
        let out = c
            .replay_call("t/1", "s1", std::slice::from_ref(&v), "why")
            .await
            .unwrap();
        assert_eq!(out["variants"][0]["status"], "completed");
        assert_eq!(log.last().body, json!({"variants": [v], "reason": "why"}));
    }

    #[tokio::test]
    async fn replay_a_run_frozen_blocked_and_the_checks_before_sending() {
        let routes = Routes::from([(
            "POST /api/v1/traces/t1/replay".to_string(),
            reply(200, json!({"status": "completed"})),
        )]);
        let (c, log) = client(routes).await;
        c.replay_run(
            "t1",
            "s2",
            &json!({"model": "m"}),
            "frozen",
            &[],
            "r",
            Signature::None,
        )
        .await
        .unwrap();
        assert_eq!(
            log.last().body,
            json!({"from_span": "s2", "edits": {"model": "m"}, "tools": "frozen", "live_tools": [], "reason": "r"})
        );
        assert!(!log.last().headers.contains_key("x-signing-token"));
        c.replay_run("t1", "s2", &json!({}), "blocked", &[], "", Signature::None)
            .await
            .unwrap();
        let n = log.len();
        let lt = vec!["lookup".to_string()];
        assert!(c
            .replay_run(
                "t1",
                "s2",
                &json!({}),
                "simulated",
                &[],
                "",
                Signature::None
            )
            .await
            .is_err());
        assert!(c
            .replay_run("t1", "s2", &json!({}), "live", &[], "", Signature::None)
            .await
            .is_err());
        assert!(c
            .replay_run("t1", "s2", &json!({}), "frozen", &lt, "", Signature::None)
            .await
            .is_err());
        assert!(c
            .replay_run(
                "t1",
                "s2",
                &json!({}),
                "frozen",
                &[],
                "",
                Signature::Password("pw")
            )
            .await
            .is_err());
        assert_eq!(log.len(), n, "nothing was sent");
    }

    #[tokio::test]
    async fn a_live_replay_signs_what_the_server_says_to_sign() {
        let routes = with(
            signing_routes(),
            &[
                (
                    "POST /api/v1/traces/t1/replay/prepare",
                    reply(
                        200,
                        json!({"gate": "live_tool_replay", "record_id": "t1:s2", "payload": {"edits_digest": "abc"}}),
                    ),
                ),
                (
                    "POST /api/v1/traces/t1/replay",
                    reply(200, json!({"status": "completed"})),
                ),
            ],
        );
        let (c, log) = client(routes).await;
        let lt = vec!["lookup".to_string()];
        c.replay_run(
            "t1",
            "s2",
            &json!({"temperature": 0}),
            "live",
            &lt,
            "r",
            Signature::Password("pw"),
        )
        .await
        .unwrap();
        let seen = log.all();
        assert_eq!(
            log.calls(),
            vec![
                "POST /api/v1/traces/t1/replay/prepare",
                "POST /api/v1/signatures/challenge",
                "POST /api/v1/signatures/token",
                "POST /api/v1/traces/t1/replay",
            ]
        );
        assert_eq!(
            seen[1].body,
            json!({"gate": "live_tool_replay", "record_id": "t1:s2", "meaning": "approved", "payload": {"edits_digest": "abc"}})
        );
        assert_eq!(seen[3].headers["x-signing-token"], "tok.sig");
        assert_eq!(seen[3].body["live_tools"], json!(["lookup"]));
        assert!(seen[3].body.get("password").is_none());
    }

    #[tokio::test]
    async fn a_token_you_hold_is_sent_as_it_is() {
        let routes = Routes::from([(
            "POST /api/v1/traces/t1/replay".to_string(),
            reply(200, json!({})),
        )]);
        let (c, log) = client(routes).await;
        let lt = vec!["lookup".to_string()];
        c.replay_run(
            "t1",
            "s2",
            &json!({}),
            "live",
            &lt,
            "",
            Signature::Token("held"),
        )
        .await
        .unwrap();
        assert_eq!(log.len(), 1);
        assert_eq!(log.last().headers["x-signing-token"], "held");
    }

    #[tokio::test]
    async fn a_span_capture_is_read_whole() {
        let routes = Routes::from([
            (
                "GET /api/v1/traces/t1/spans/s1/capture".to_string(),
                reply(200, json!({"response": {"content": "ok"}})),
            ),
            (
                "GET /api/v1/traces/t1/spans/old/capture".to_string(),
                reply(404, json!({"error": "not_captured", "message": "no"})),
            ),
        ]);
        let (c, _) = client(routes).await;
        assert_eq!(
            c.span_capture("t1", "s1").await.unwrap()["response"]["content"],
            "ok"
        );
        let e = c.span_capture("t1", "old").await.unwrap_err();
        assert_eq!(e.downcast_ref::<ApiError>().unwrap().status, 404);
    }

    #[tokio::test]
    async fn datasets_create_list_get_update_with_the_mode_checked() {
        let routes = Routes::from([
            (
                "POST /api/v1/datasets".to_string(),
                reply(201, json!({"id": "ds1"})),
            ),
            (
                "GET /api/v1/datasets".to_string(),
                reply(200, json!([{"id": "ds1"}])),
            ),
            (
                "GET /api/v1/datasets/ds1".to_string(),
                reply(200, json!({"id": "ds1"})),
            ),
            (
                "PUT /api/v1/datasets/ds1".to_string(),
                reply(200, json!({"id": "ds1"})),
            ),
        ]);
        let (c, log) = client(routes).await;
        let body =
            json!({"name": "evals", "retention_mode": "regulated", "min_retention_days": 365});
        c.create_dataset(&body).await.unwrap();
        assert_eq!(log.last().body, body);
        assert_eq!(c.list_datasets().await.unwrap().len(), 1);
        assert_eq!(c.get_dataset("ds1").await.unwrap()["id"], "ds1");
        c.update_dataset(
            "ds1",
            &json!({"description": "d", "retention_mode": "expire", "ttl_days": 30}),
        )
        .await
        .unwrap();
        let n = log.len();
        assert!(c
            .create_dataset(&json!({"name": "x", "retention_mode": "forever"}))
            .await
            .is_err());
        assert!(c
            .update_dataset("ds1", &json!({"retention_mode": "keep"}))
            .await
            .is_err());
        assert_eq!(log.len(), n);
    }

    #[tokio::test]
    async fn cases_are_added_listed_retired_and_erased() {
        let routes = Routes::from([
            (
                "POST /api/v1/datasets/ds1/cases".to_string(),
                reply(201, json!({"id": "c1"})),
            ),
            (
                "POST /api/v1/datasets/ds1/cases/from-trace".to_string(),
                reply(201, json!({"id": "c2"})),
            ),
            (
                "GET /api/v1/datasets/ds1/cases?retired=true&limit=10&offset=20".to_string(),
                reply(200, json!([])),
            ),
            (
                "GET /api/v1/datasets/ds1/cases".to_string(),
                reply(200, json!([{"id": "c1"}])),
            ),
            (
                "DELETE /api/v1/datasets/ds1/cases/c1".to_string(),
                reply(204, json!({})),
            ),
            (
                "POST /api/v1/datasets/ds1/cases/c1/erase".to_string(),
                reply(
                    200,
                    json!({"case": {"erased": true}, "already_erased": false}),
                ),
            ),
        ]);
        let (c, log) = client(routes).await;
        c.add_dataset_case("ds1", &json!({"input": {"q": 1}}))
            .await
            .unwrap();
        assert_eq!(log.last().body, json!({"input": {"q": 1}}));
        let from_trace = json!({"trace_id": "t1", "span_id": "s1", "expected_output": null});
        c.add_dataset_case_from_trace("ds1", &from_trace)
            .await
            .unwrap();
        assert_eq!(
            log.last().body,
            from_trace,
            "an expected_output of null is sent, not dropped"
        );
        c.list_dataset_cases("ds1", true, Some(10), Some(20))
            .await
            .unwrap();
        assert_eq!(
            c.list_dataset_cases("ds1", false, None, None)
                .await
                .unwrap()[0]["id"],
            "c1"
        );
        c.retire_dataset_case("ds1", "c1").await.unwrap();
        assert_eq!(log.last().method, "DELETE");
        assert_eq!(
            c.erase_dataset_case("ds1", "c1", "gdpr").await.unwrap()["case"]["erased"],
            true
        );
        assert_eq!(log.last().body, json!({"reason": "gdpr"}));
    }

    #[tokio::test]
    async fn versions_are_listed_fetched_verified() {
        let routes = Routes::from([
            (
                "GET /api/v1/datasets/ds1/versions".to_string(),
                reply(200, json!([{"version": 1}])),
            ),
            (
                "GET /api/v1/datasets/ds1/versions/2".to_string(),
                reply(200, json!({"version": 2})),
            ),
            (
                "GET /api/v1/datasets/ds1/versions/2/verify".to_string(),
                reply(200, json!({"ok": true})),
            ),
        ]);
        let (c, _) = client(routes).await;
        assert_eq!(c.list_dataset_versions("ds1").await.unwrap().len(), 1);
        assert_eq!(c.get_dataset_version("ds1", 2).await.unwrap()["version"], 2);
        assert_eq!(
            c.verify_dataset_version("ds1", 2).await.unwrap()["ok"],
            true
        );
    }

    #[tokio::test]
    async fn publishing_a_regulated_version_is_signed_and_an_ordinary_one_is_not() {
        let routes = with(
            signing_routes(),
            &[(
                "POST /api/v1/datasets/ds1/versions",
                reply(201, json!({"signed": true})),
            )],
        );
        let (c, log) = client(routes).await;
        c.publish_dataset("ds1", Signature::Password("pw"))
            .await
            .unwrap();
        assert_eq!(
            log.all()[0].body,
            json!({"gate": "dataset_publish", "record_id": "ds1", "meaning": "approved", "payload": {}})
        );
        assert_eq!(log.last().headers["x-signing-token"], "tok.sig");
        c.publish_dataset("ds1", Signature::None).await.unwrap();
        assert!(!log.last().headers.contains_key("x-signing-token"));
    }

    #[tokio::test]
    async fn export_returns_the_manifest_the_cases_and_the_text() {
        let manifest = json!({"type": "manifest", "version": 2, "manifest_digest": "abc"});
        let case = json!({"type": "case", "position": 1, "content_hash": "h1"});
        let text = format!("{manifest}\n{case}\n");
        let routes = Routes::from([(
            "GET /api/v1/datasets/ds1/export?version=2".to_string(),
            Reply::Raw(
                200,
                text.clone(),
                vec![("X-Manifest-Digest".into(), "abc".into())],
            ),
        )]);
        let (c, _) = client(routes).await;
        let out = c.export_dataset("ds1", Some(2)).await.unwrap();
        assert_eq!((out.version, out.manifest_digest.as_str()), (2, "abc"));
        assert_eq!(out.cases, vec![case]);
        assert_eq!(out.text, text);
    }

    #[tokio::test]
    async fn lowering_a_minimum_and_purging_are_signed_on_the_purge_gate() {
        let routes = with(
            signing_routes(),
            &[
                (
                    "POST /api/v1/datasets/ds1/retention/lower-minimum",
                    reply(200, json!({"id": "ds1"})),
                ),
                (
                    "POST /api/v1/datasets/ds1/purge",
                    reply(200, json!({"signed": true})),
                ),
            ],
        );
        let (c, log) = client(routes).await;
        c.lower_dataset_minimum("ds1", 30, "policy", Signature::Password("pw"))
            .await
            .unwrap();
        let seen = log.all();
        assert_eq!(seen[0].body["gate"], "dataset_purge");
        assert_eq!(
            seen[0].body["payload"],
            json!({"action": "lower_minimum", "new_minimum_days": 30, "reason": "policy"})
        );
        assert_eq!(
            seen[2].body,
            json!({"min_retention_days": 30, "reason": "policy"})
        );
        assert_eq!(seen[2].headers["x-signing-token"], "tok.sig");
        c.purge_dataset("ds1", "done", Signature::Password("pw"))
            .await
            .unwrap();
        assert_eq!(
            log.all()[3].body["payload"],
            json!({"action": "purge", "reason": "done"})
        );
        assert_eq!(log.last().body, json!({"reason": "done"}));
    }

    #[tokio::test]
    async fn placing_a_hold_is_unsigned_and_lifting_one_is_the_legal_gate() {
        let routes = with(
            signing_routes(),
            &[
                (
                    "PUT /api/v1/datasets/ds1/legal-hold",
                    reply(200, json!({"id": "ds1"})),
                ),
                (
                    "POST /api/v1/audit/legal-hold",
                    reply(200, json!({"hold": false})),
                ),
            ],
        );
        let (c, log) = client(routes).await;
        c.set_dataset_legal_hold("ds1", true, "lit", Signature::Password("pw"))
            .await
            .unwrap();
        assert_eq!(log.len(), 1, "placing signs nothing");
        assert_eq!(log.last().body, json!({"held": true, "reason": "lit"}));

        c.set_dataset_legal_hold("ds1", false, "closed", Signature::Password("pw"))
            .await
            .unwrap();
        let seen = log.all();
        assert_eq!(
            seen[1].body,
            json!({"gate": "legal_hold_release", "record_id": "dataset:ds1", "meaning": "approved", "payload": {"reason": "closed"}})
        );
        assert_eq!(seen[3].method, "PUT");
        assert_eq!(seen[3].body, json!({"held": false, "reason": "closed"}));
        assert_eq!(seen[3].headers["x-signing-token"], "tok.sig");

        c.set_audit_legal_hold(false, "closed", Signature::Password("pw"))
            .await
            .unwrap();
        let seen = log.all();
        assert_eq!(seen[4].body["record_id"], "audit:acme");
        assert_eq!(log.last().body, json!({"hold": false, "reason": "closed"}));
        assert_eq!(log.last().headers["x-signing-token"], "tok.sig");
    }

    #[tokio::test]
    async fn expiring_reviews_and_tombstones() {
        let routes = Routes::from([
            (
                "GET /api/v1/datasets/expiring".to_string(),
                reply(200, json!([{"id": "a"}])),
            ),
            (
                "GET /api/v1/datasets/expiring?days=7".to_string(),
                reply(200, json!([])),
            ),
            (
                "GET /api/v1/datasets/reviews?days=14".to_string(),
                reply(200, json!([{"id": "b"}])),
            ),
            (
                "GET /api/v1/datasets/tombstones".to_string(),
                reply(200, json!([{"dataset": "old"}])),
            ),
        ]);
        let (c, log) = client(routes).await;
        assert_eq!(c.list_expiring_datasets(None).await.unwrap().len(), 1);
        c.list_expiring_datasets(Some(7)).await.unwrap();
        assert_eq!(log.last().path, "/api/v1/datasets/expiring?days=7");
        assert_eq!(c.list_dataset_reviews(Some(14)).await.unwrap().len(), 1);
        assert_eq!(
            c.list_dataset_tombstones().await.unwrap()[0]["dataset"],
            "old"
        );
    }

    #[tokio::test]
    async fn signatures_and_the_approver_policy() {
        let routes = Routes::from([
            (
                "GET /api/v1/signatures?gate=dataset_purge&record_id=ds1".to_string(),
                reply(200, json!([{"id": "s1"}])),
            ),
            (
                "GET /api/v1/signatures/s1".to_string(),
                reply(200, json!({"id": "s1"})),
            ),
            (
                "GET /api/v1/signatures/verify".to_string(),
                reply(200, json!({"ok": true})),
            ),
            (
                "GET /api/v1/approver-policy".to_string(),
                reply(200, json!({"policy": {}, "gates": ["tool_approval"]})),
            ),
            (
                "PUT /api/v1/approver-policy".to_string(),
                reply(200, json!({"policy": {}})),
            ),
        ]);
        let (c, log) = client(routes).await;
        assert_eq!(
            c.list_signatures("dataset_purge", "ds1")
                .await
                .unwrap()
                .len(),
            1
        );
        assert_eq!(c.get_signature("s1").await.unwrap()["id"], "s1");
        assert_eq!(c.verify_signatures().await.unwrap()["ok"], true);
        assert_eq!(
            c.approver_policy().await.unwrap()["gates"][0],
            "tool_approval"
        );
        c.set_approver_policy(
            &json!({"environment_promotion:prod": {"roles": ["head_chef", "legal"]}}),
        )
        .await
        .unwrap();
        assert_eq!(
            log.last().body,
            json!({"policy": {"environment_promotion:prod": {"roles": ["head_chef", "legal"], "users": []}}})
        );
        let n = log.len();
        assert!(c
            .set_approver_policy(&json!({"tool_approval": {"roles": ["admin"]}}))
            .await
            .is_err());
        assert!(c
            .set_approver_policy(&json!({"tool_approval": {"roles": ["sous_chef"]}}))
            .await
            .is_err());
        assert_eq!(log.len(), n);
    }

    #[tokio::test]
    async fn a_run_that_skips_an_approval_signs_recipe_at_environment() {
        let routes = with(
            signing_routes(),
            &[(
                "POST /api/v1/recipes/refunds/bake",
                reply(200, json!({"run_id": "r9"})),
            )],
        );
        let (c, log) = client(routes).await;
        let out = c
            .run_recipe(
                "refunds",
                &json!({"order": 7}),
                Some("prod"),
                Signature::Password("pw"),
            )
            .await
            .unwrap();
        assert_eq!(out["run_id"], "r9");
        let seen = log.all();
        assert_eq!(seen[0].body["gate"], "approval_skip");
        assert_eq!(seen[0].body["record_id"], "refunds@prod");
        assert_eq!(seen[0].body["payload"], json!({"input": {"order": 7}}));
        assert_eq!(
            seen[2].body,
            json!({"input": {"order": 7}, "environment": "prod"})
        );
        assert_eq!(seen[2].headers["x-signing-token"], "tok.sig");
        c.run_recipe("refunds", &json!({}), Some("dev"), Signature::None)
            .await
            .unwrap();
        assert!(!log.last().headers.contains_key("x-signing-token"));
        assert!(c
            .run_recipe("refunds", &json!({}), None, Signature::Password("pw"))
            .await
            .is_err());
    }

    #[tokio::test]
    async fn bindings_owners_and_my_kitchens() {
        let routes = Routes::from([
            (
                "GET /api/v1/kitchens/k1/bindings".to_string(),
                reply(200, json!([{"id": "b1", "role": "chef"}])),
            ),
            (
                "POST /api/v1/kitchens/k1/bindings".to_string(),
                reply(201, json!({"id": "b2"})),
            ),
            (
                "DELETE /api/v1/kitchens/k1/bindings/b2".to_string(),
                reply(204, json!({})),
            ),
            (
                "POST /api/v1/service-accounts/sa1/owner".to_string(),
                reply(200, json!({"owner_email": "ana@acme.io"})),
            ),
            (
                "POST /api/v1/keys/k9/owner".to_string(),
                reply(200, json!({"owner_email": "bo@acme.io"})),
            ),
            (
                "GET /api/v1/me/kitchens".to_string(),
                reply(
                    200,
                    json!({"enforced": true, "kitchens": [{"role": "head_chef"}]}),
                ),
            ),
        ]);
        let (c, log) = client(routes).await;
        assert_eq!(
            c.list_kitchen_bindings("k1").await.unwrap()[0]["role"],
            "chef"
        );
        c.create_kitchen_binding("k1", "aad-guid", "taster", "QA")
            .await
            .unwrap();
        assert_eq!(
            log.last().body,
            json!({"claim_value": "aad-guid", "role": "taster", "label": "QA"})
        );
        c.delete_kitchen_binding("k1", "b2").await.unwrap();
        c.set_service_account_owner("sa1", "ana@acme.io")
            .await
            .unwrap();
        assert_eq!(log.last().body, json!({"owner_email": "ana@acme.io"}));
        c.set_scoped_key_owner("k9", "bo@acme.io").await.unwrap();
        assert_eq!(log.last().path, "/api/v1/keys/k9/owner");
        assert_eq!(
            c.my_kitchens().await.unwrap()["kitchens"][0]["role"],
            "head_chef"
        );

        let n = log.len();
        for role in ["admin", "restaurateur", "sous_chef"] {
            assert!(
                c.create_kitchen_binding("k1", "g", role, "").await.is_err(),
                "{role}"
            );
        }
        assert!(c
            .create_kitchen_binding("k1", "  ", "chef", "")
            .await
            .is_err());
        assert_eq!(log.len(), n);
    }

    #[tokio::test]
    async fn a_server_refusal_surfaces_its_code() {
        let routes = Routes::from([(
            "POST /api/v1/kitchens/k1/bindings".to_string(),
            reply(
                403,
                json!({"error": "binding_head_chef_requires_restaurateur", "message": "m"}),
            ),
        )]);
        let (c, _) = client(routes).await;
        let e = c
            .create_kitchen_binding("k1", "g", "head_chef", "")
            .await
            .unwrap_err();
        assert_eq!(
            e.downcast_ref::<ApiError>().unwrap().code,
            "binding_head_chef_requires_restaurateur"
        );
    }

    #[tokio::test]
    async fn one_agent_version_is_read() {
        let routes = Routes::from([(
            "GET /api/v1/agents/refund-bot/versions/0.1.0".to_string(),
            reply(200, json!({"name": "refund-bot", "version": "0.1.0"})),
        )]);
        let (c, _) = client(routes).await;
        assert_eq!(
            c.agent_version("refund-bot", "0.1.0").await.unwrap()["version"],
            "0.1.0"
        );
    }
}
