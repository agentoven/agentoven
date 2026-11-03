//! Document intelligence (ADR-0039): read what a file says, transcribe scans, and pull fields out of a
//! document together with the quote each came from.
//!
//! `extract` reads Word, Excel, HTML, CSV/TSV, Markdown, RTF, plain text and a PDF's text layer in the
//! control plane, with no model; a page with no text layer is listed in `empty_pages` and `needs_ocr` is
//! true. `ocr` and `fields` send the document to the one provider you name, through the router, so budgets,
//! rate limits and traces apply. A refusal is an [`super::pro::ApiError`] with its status.

use reqwest::Method;
use serde_json::{json, Value};

use super::AgentOvenClient;

impl AgentOvenClient {
    async fn post_json(&self, path: &str, body: &Value) -> anyhow::Result<Value> {
        self.call_json(Method::POST, path, Some(body), None).await
    }

    /// Read a stored file (`POST /api/v1/documents/extract` with its `blob_ref`). `max_text_bytes` cuts the
    /// text shorter than the server's default.
    pub async fn extract_document(
        &self,
        blob_ref: &str,
        max_text_bytes: Option<u64>,
    ) -> anyhow::Result<Value> {
        let mut body = json!({ "blob_ref": blob_ref });
        if let Some(n) = max_text_bytes {
            body["max_text_bytes"] = json!(n);
        }
        self.post_json("/api/v1/documents/extract", &body).await
    }

    /// Read a file sent with the request and not stored. `name`'s extension and `mime_type` tell the server
    /// the format; the type wins unless it says nothing (octet-stream, zip, plain text).
    pub async fn extract_document_bytes(
        &self,
        name: &str,
        mime_type: &str,
        data: Vec<u8>,
    ) -> anyhow::Result<Value> {
        let query: String = url::form_urlencoded::Serializer::new(String::new())
            .append_pair("name", name)
            .finish();
        let path = format!("/api/v1/documents/extract?{query}");
        self.call_bytes(&path, data, mime_type).await
    }

    /// Transcribe an image or a scanned PDF through `provider`, a page per call. `verify` is
    /// `{"provider", "model", "min_agreement"}` for a second reading by another model.
    pub async fn ocr_document(
        &self,
        blob_ref: &str,
        provider: &str,
        model: Option<&str>,
        max_pages: Option<u32>,
        verify: Option<Value>,
    ) -> anyhow::Result<Value> {
        let mut body = json!({ "blob_ref": blob_ref, "provider": provider });
        if let Some(m) = model {
            body["model"] = json!(m);
        }
        if let Some(n) = max_pages {
            body["max_pages"] = json!(n);
        }
        if let Some(v) = verify {
            body["verify"] = v;
        }
        self.post_json("/api/v1/documents/ocr", &body).await
    }

    /// Read a flat JSON Schema's properties out of a document. `body` is the request as the server takes
    /// it: one of `blob_ref` and `text`, `schema`, `provider`, and optionally `model`, `instructions`, `ocr`.
    pub async fn extract_document_fields(&self, body: &Value) -> anyhow::Result<Value> {
        self.post_json("/api/v1/documents/fields", body).await
    }
}
