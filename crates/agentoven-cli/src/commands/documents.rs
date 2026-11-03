//! `agentoven documents` — read a file's text and tables, transcribe scans, extract fields (ADR-0039).
//!
//! `extract` reads Word, Excel, HTML, CSV/TSV, Markdown, RTF, plain text and a PDF's text layer without a
//! model and prints the text a model would be shown (stdout), with the reader's notes on stderr. `ocr` and
//! `fields` send the document to the one provider you name, through the control plane's router, so your
//! budgets and rate limits apply. The confidence OCR prints says what was checked, not a probability:
//! `unverified` is one model's reading; `--verify-provider/--verify-model` asks a second model.
//! `--json` (or `--output json`) prints the server's JSON as it is.

use agentoven_core::AgentOvenClient;
use clap::{Args, Subcommand};
use colored::Colorize;
use serde_json::{json, Value};

use super::budgets::refusal_hint;
use super::signing::{clip, pretty};

#[derive(Subcommand)]
pub enum DocumentsCommands {
    /// Read a file's text and tables: a local file (sent for this call, not stored) or a stored blob.
    Extract(ExtractArgs),
    /// Transcribe an image or a scanned PDF, a page per model call, with the provider you name.
    Ocr(OcrArgs),
    /// Read the properties of a JSON Schema out of a document; each value comes with its quote.
    Fields(FieldsArgs),
}

#[derive(Args)]
pub struct ExtractArgs {
    /// A local file, or with --blob-ref the reference of a stored one (a bare 64-digit reference that is not
    /// a file is taken as one too).
    pub source: String,
    /// `source` is a blob reference (from `POST /api/v1/blobs`), not a path.
    #[arg(long)]
    pub blob_ref: bool,
    /// Cut the text at this many bytes (stored files only).
    #[arg(long)]
    pub max_text_bytes: Option<u64>,
    /// Print the full JSON result (blocks, sheets, pages, warnings).
    #[arg(long)]
    pub json: bool,
}

#[derive(Args)]
pub struct OcrArgs {
    /// The blob reference of an image or a PDF.
    pub blob_ref: String,
    /// The provider that reads the pages; the document is sent nowhere else.
    #[arg(long)]
    pub provider: String,
    #[arg(long)]
    pub model: Option<String>,
    /// At most this many pages without a text layer are read (the server's default is 10).
    #[arg(long)]
    pub max_pages: Option<u32>,
    /// Read every page again with this provider (default: --provider) and a different model.
    #[arg(long, requires = "verify_model")]
    pub verify_provider: Option<String>,
    /// The model of the second reading; it must differ from the first reader.
    #[arg(long)]
    pub verify_model: Option<String>,
    /// How alike (0 to 1) the two readings must be to count as agreeing (0.97 when left out).
    #[arg(long)]
    pub min_agreement: Option<f64>,
    #[arg(long)]
    pub json: bool,
}

#[derive(Args)]
pub struct FieldsArgs {
    /// The blob reference of the document (or give --text).
    #[arg(conflicts_with = "text", required_unless_present = "text")]
    pub blob_ref: Option<String>,
    /// Text to read the fields from instead of a stored document.
    #[arg(long)]
    pub text: Option<String>,
    /// The JSON Schema: a file, `@file`, or JSON on the command line. Flat: strings, numbers, integers,
    /// booleans, enums and arrays of those; "required" lists the fields that must be found.
    #[arg(long)]
    pub schema: String,
    #[arg(long)]
    pub provider: String,
    #[arg(long)]
    pub model: Option<String>,
    #[arg(long)]
    pub instructions: Option<String>,
    /// Read a scan first, with the same provider and model.
    #[arg(long)]
    pub ocr: bool,
    /// With --ocr: at most this many pages without a text layer.
    #[arg(long, requires = "ocr")]
    pub max_pages: Option<u32>,
    /// Exit with an error unless every field asked for is grounded in the document.
    #[arg(long)]
    pub strict: bool,
    #[arg(long)]
    pub json: bool,
}

/// What a command prints: stdout is the result (safe to pipe), stderr the reader's notes.
#[derive(Default)]
pub struct Output {
    pub out: String,
    pub notes: String,
}

pub async fn execute(
    cmd: DocumentsCommands,
    client: AgentOvenClient,
    json: bool,
) -> anyhow::Result<()> {
    match run(cmd, &client, json).await {
        Ok(o) => {
            eprint!("{}", o.notes);
            print!("{}", o.out);
            Ok(())
        }
        Err(e) => {
            if let Some(h) = refusal_hint(&e) {
                eprintln!("{h}");
            }
            Err(e)
        }
    }
}

fn is_blob_ref(s: &str) -> bool {
    s.len() == 64 && s.bytes().all(|b| b.is_ascii_hexdigit())
}

fn notes(v: &Value) -> String {
    let mut out = String::new();
    for w in v["warnings"].as_array().into_iter().flatten() {
        out.push_str(&format!(
            "{} {}\n",
            "note:".yellow(),
            w.as_str().unwrap_or_default()
        ));
    }
    out
}

fn schema_arg(v: &str) -> anyhow::Result<Value> {
    let text = if v.trim_start().starts_with('{') {
        v.to_string()
    } else {
        let path = v.strip_prefix('@').unwrap_or(v);
        std::fs::read_to_string(path)
            .map_err(|e| anyhow::anyhow!("--schema: cannot read {path}: {e}"))?
    };
    serde_json::from_str(&text).map_err(|e| anyhow::anyhow!("--schema is not valid JSON: {e}"))
}

pub async fn run(
    cmd: DocumentsCommands,
    client: &AgentOvenClient,
    json: bool,
) -> anyhow::Result<Output> {
    match cmd {
        DocumentsCommands::Extract(a) => {
            let json = json || a.json;
            let path = std::path::Path::new(&a.source);
            let v = if a.blob_ref || (!path.is_file() && is_blob_ref(&a.source)) {
                client.extract_document(&a.source, a.max_text_bytes).await?
            } else {
                if a.max_text_bytes.is_some() {
                    anyhow::bail!("--max-text-bytes applies to a stored file (--blob-ref); an upload is cut at the server's default");
                }
                let data = std::fs::read(path)
                    .map_err(|e| anyhow::anyhow!("cannot read {}: {e}", a.source))?;
                let name = path
                    .file_name()
                    .map(|n| n.to_string_lossy().to_string())
                    .unwrap_or_default();
                client
                    .extract_document_bytes(&name, "application/octet-stream", data)
                    .await?
            };
            if json {
                return Ok(Output {
                    out: pretty(&v) + "\n",
                    ..Default::default()
                });
            }
            let mut n = notes(&v);
            if v["needs_ocr"].as_bool().unwrap_or(false) {
                n.push_str(&format!(
                    "{} some pages are images of text; read them with: agentoven documents ocr <blob-ref> --provider <name>\n",
                    "note:".yellow()
                ));
            }
            let text = v["text"].as_str().unwrap_or_default();
            Ok(Output {
                out: format!("{text}\n"),
                notes: n,
            })
        }
        DocumentsCommands::Ocr(a) => {
            let json = json || a.json;
            let verify = a.verify_model.as_ref().map(|m| {
                let mut v = json!({
                    "provider": a.verify_provider.clone().unwrap_or_else(|| a.provider.clone()),
                    "model": m,
                });
                if let Some(x) = a.min_agreement {
                    v["min_agreement"] = json!(x);
                }
                v
            });
            let v = client
                .ocr_document(
                    &a.blob_ref,
                    &a.provider,
                    a.model.as_deref(),
                    a.max_pages,
                    verify,
                )
                .await?;
            if json {
                return Ok(Output {
                    out: pretty(&v) + "\n",
                    ..Default::default()
                });
            }
            let mut n = notes(&v);
            n.push_str(&format!(
                "{} {} via {}{}{}\n",
                "confidence:".bold(),
                v["confidence"].as_str().unwrap_or("-"),
                v["provider"].as_str().unwrap_or("-"),
                v["usage"]["estimated_cost_usd"]
                    .as_f64()
                    .map(|c| format!(", ${c:.4}"))
                    .unwrap_or_default(),
                if v["incomplete"].as_bool().unwrap_or(false) {
                    " — INCOMPLETE: some pages were not read".red().to_string()
                } else {
                    String::new()
                },
            ));
            let text = v["text"].as_str().unwrap_or_default();
            Ok(Output {
                out: format!("{text}\n"),
                notes: n,
            })
        }
        DocumentsCommands::Fields(a) => {
            let json = json || a.json;
            let mut body = json!({"schema": schema_arg(&a.schema)?, "provider": a.provider});
            match (&a.blob_ref, &a.text) {
                (Some(r), None) => body["blob_ref"] = json!(r),
                (None, Some(t)) => body["text"] = json!(t),
                _ => anyhow::bail!("give exactly one of a blob reference and --text"),
            }
            if let Some(m) = &a.model {
                body["model"] = json!(m);
            }
            if let Some(i) = &a.instructions {
                body["instructions"] = json!(i);
            }
            if a.ocr {
                body["ocr"] = match a.max_pages {
                    Some(n) => json!({"max_pages": n}),
                    None => json!({}),
                };
            }
            let v = client.extract_document_fields(&body).await?;
            let grounded = v["all_grounded"].as_bool().unwrap_or(false);
            let rendered = if json {
                pretty(&v) + "\n"
            } else {
                fields_table(&v)
            };
            if a.strict && !grounded {
                anyhow::bail!("not every field is grounded in the document (--strict)\n{rendered}");
            }
            Ok(Output {
                out: rendered,
                notes: if json { String::new() } else { notes(&v) },
            })
        }
    }
}

fn fields_table(v: &Value) -> String {
    let mut out = String::from("\n");
    for f in v["fields"].as_array().into_iter().flatten() {
        let status = f["status"].as_str().unwrap_or("-");
        let colored = match status {
            "grounded" => status.green().bold(),
            "ungrounded" => status.red().bold(),
            _ => status.dimmed().bold(),
        };
        let value = match &f["value"] {
            Value::Null => "-".to_string(),
            Value::String(s) => s.clone(),
            other => other.to_string(),
        };
        out.push_str(&format!(
            "  {:<22} {:<11} {}",
            f["name"].as_str().unwrap_or("-").bold(),
            colored,
            clip(&value, 60)
        ));
        if let Some(p) = f["page"].as_i64().filter(|p| *p > 0) {
            out.push_str(&format!("  (page {p})"));
        }
        out.push('\n');
        if let Some(q) = f["quote"].as_str().filter(|q| !q.is_empty()) {
            out.push_str(&format!("      quote: \"{}\"\n", clip(q, 100)));
        }
        let flags: Vec<&str> = f["flags"]
            .as_array()
            .into_iter()
            .flatten()
            .filter_map(|x| x.as_str())
            .collect();
        if !flags.is_empty() {
            out.push_str(&format!(
                "      {} {}\n",
                "flags:".yellow(),
                flags.join(", ")
            ));
        }
    }
    out.push_str(&format!(
        "\n  {}\n",
        if v["all_grounded"].as_bool().unwrap_or(false) {
            "Every field is grounded in the document's text.".to_string()
        } else {
            "Not every field is grounded: check the flagged ones against the source before using them.".to_string()
        }
    ));
    out
}

#[cfg(test)]
mod tests {
    use super::*;
    use agentoven_core::testing::{fake_control_plane, json as reply, Routes};

    const REF: &str = "abababababababababababababababababababababababababababababababab";

    fn extract_args(source: &str) -> ExtractArgs {
        ExtractArgs {
            source: source.into(),
            blob_ref: false,
            max_text_bytes: None,
            json: false,
        }
    }

    #[tokio::test]
    async fn extract_sends_a_local_file_as_the_raw_body_and_prints_its_text() {
        colored::control::set_override(false);
        let (base, log) = fake_control_plane(Routes::from([(
            "POST /api/v1/documents/extract?name=claim.docx".to_string(),
            reply(200, json!({"format": "docx", "text": "# Claim\n\nTotal 420", "warnings": ["sheet \"H\" is hidden and was not read"]})),
        )]))
        .await;
        let dir = std::env::temp_dir().join(format!("ao-docs-{}", std::process::id()));
        std::fs::create_dir_all(&dir).unwrap();
        let file = dir.join("claim.docx");
        std::fs::write(&file, b"PK\x03\x04").unwrap();
        let c = AgentOvenClient::new(&base).unwrap();
        let o = run(
            DocumentsCommands::Extract(extract_args(file.to_str().unwrap())),
            &c,
            false,
        )
        .await
        .unwrap();
        assert_eq!(o.out, "# Claim\n\nTotal 420\n");
        assert!(o.notes.contains("is hidden"), "{}", o.notes);
        let seen = log.last();
        assert_eq!(
            seen.headers.get("content-type").map(String::as_str),
            Some("application/octet-stream")
        );
        assert!(run(
            DocumentsCommands::Extract(extract_args("/no/such/file.docx")),
            &c,
            false
        )
        .await
        .is_err());
        let mut cut = extract_args(file.to_str().unwrap());
        cut.max_text_bytes = Some(10);
        assert!(
            run(DocumentsCommands::Extract(cut), &c, false)
                .await
                .is_err(),
            "an upload cannot be cut"
        );
        std::fs::remove_dir_all(&dir).ok();
    }

    #[tokio::test]
    async fn extract_of_a_stored_file_sends_its_reference_and_points_at_ocr_for_scans() {
        colored::control::set_override(false);
        let (base, log) = fake_control_plane(Routes::from([(
            "POST /api/v1/documents/extract".to_string(),
            reply(200, json!({"format": "pdf", "text": "[Page 1]\nCover", "needs_ocr": true, "empty_pages": [2]})),
        )]))
        .await;
        let c = AgentOvenClient::new(&base).unwrap();
        let mut a = extract_args(REF);
        a.max_text_bytes = Some(500);
        let o = run(DocumentsCommands::Extract(a), &c, false).await.unwrap();
        assert_eq!(
            log.last().body,
            json!({"blob_ref": REF, "max_text_bytes": 500})
        );
        assert!(o.notes.contains("documents ocr"), "{}", o.notes);
        // A bare reference that is not a file is a reference.
        run(DocumentsCommands::Extract(extract_args(REF)), &c, true)
            .await
            .unwrap();
        let o = run(
            DocumentsCommands::Extract(ExtractArgs {
                json: true,
                ..extract_args(REF)
            }),
            &c,
            false,
        )
        .await
        .unwrap();
        assert!(o.out.contains("\"empty_pages\""), "{}", o.out);
    }

    #[tokio::test]
    async fn ocr_names_the_provider_the_second_pass_and_says_how_sure_it_is_not() {
        colored::control::set_override(false);
        let (base, log) = fake_control_plane(Routes::from([(
            "POST /api/v1/documents/ocr".to_string(),
            reply(200, json!({"text": "[Page 1]\nInvoice 7", "confidence": "agreed", "provider": "vision", "incomplete": true,
                              "usage": {"estimated_cost_usd": 0.0123}, "warnings": ["page 3 was not read"]})),
        )]))
        .await;
        let c = AgentOvenClient::new(&base).unwrap();
        let o = run(
            DocumentsCommands::Ocr(OcrArgs {
                blob_ref: REF.into(),
                provider: "vision".into(),
                model: Some("gpt-4o".into()),
                max_pages: Some(3),
                verify_provider: None,
                verify_model: Some("other".into()),
                min_agreement: Some(0.9),
                json: false,
            }),
            &c,
            false,
        )
        .await
        .unwrap();
        assert_eq!(
            log.last().body,
            json!({"blob_ref": REF, "provider": "vision", "model": "gpt-4o", "max_pages": 3,
                   "verify": {"provider": "vision", "model": "other", "min_agreement": 0.9}})
        );
        assert_eq!(o.out, "[Page 1]\nInvoice 7\n");
        for want in [
            "confidence: agreed via vision",
            "$0.0123",
            "INCOMPLETE",
            "page 3 was not read",
        ] {
            assert!(o.notes.contains(want), "{want} missing from {}", o.notes);
        }
    }

    fn fields_args(blob: Option<&str>, text: Option<&str>, schema: &str) -> FieldsArgs {
        FieldsArgs {
            blob_ref: blob.map(Into::into),
            text: text.map(Into::into),
            schema: schema.into(),
            provider: "vision".into(),
            model: None,
            instructions: None,
            ocr: false,
            max_pages: None,
            strict: false,
            json: false,
        }
    }

    #[tokio::test]
    async fn fields_show_each_value_with_its_quote_and_strict_fails_on_an_ungrounded_one() {
        colored::control::set_override(false);
        let (base, log) = fake_control_plane(Routes::from([(
            "POST /api/v1/documents/fields".to_string(),
            reply(200, json!({"all_grounded": false, "fields": [
                {"name": "total", "value": 420.0, "status": "grounded", "page": 2, "quote": "Total due 420.00"},
                {"name": "holder", "value": "Asha", "status": "ungrounded", "quote": "Policy holder: Asha", "flags": ["quote_not_found"]}]})),
        )]))
        .await;
        let c = AgentOvenClient::new(&base).unwrap();
        let schema = r#"{"type":"object","properties":{"total":{"type":"number"}}}"#;
        let mut a = fields_args(Some(REF), None, schema);
        a.ocr = true;
        a.max_pages = Some(4);
        a.instructions = Some("USD".into());
        let o = run(DocumentsCommands::Fields(a), &c, false).await.unwrap();
        assert_eq!(
            log.last().body,
            json!({"schema": {"type": "object", "properties": {"total": {"type": "number"}}}, "provider": "vision",
                   "blob_ref": REF, "instructions": "USD", "ocr": {"max_pages": 4}})
        );
        for want in [
            "total",
            "420",
            "(page 2)",
            "quote: \"Total due 420.00\"",
            "ungrounded",
            "flags: quote_not_found",
            "Not every field is grounded",
        ] {
            assert!(o.out.contains(want), "{want} missing from {}", o.out);
        }
        let mut strict = fields_args(None, Some("Total 420"), schema);
        strict.strict = true;
        let err = run(DocumentsCommands::Fields(strict), &c, false)
            .await
            .err()
            .expect("strict must fail");
        assert!(err.to_string().contains("--strict"), "{err}");
        assert_eq!(log.last().body["text"], json!("Total 420"));
        assert!(log.last().body.get("blob_ref").is_none());
        assert!(run(
            DocumentsCommands::Fields(fields_args(Some(REF), None, "{not json")),
            &c,
            false
        )
        .await
        .is_err());
        assert!(run(
            DocumentsCommands::Fields(fields_args(Some(REF), None, "/no/such/schema.json")),
            &c,
            false
        )
        .await
        .is_err());
    }

    #[tokio::test]
    async fn a_refusal_keeps_its_status() {
        let (base, _) = fake_control_plane(Routes::from([(
            "POST /api/v1/documents/ocr".to_string(),
            reply(404, json!({"error": "blob not found"})),
        )]))
        .await;
        let c = AgentOvenClient::new(&base).unwrap();
        let err = run(
            DocumentsCommands::Ocr(OcrArgs {
                blob_ref: REF.into(),
                provider: "v".into(),
                model: None,
                max_pages: None,
                verify_provider: None,
                verify_model: None,
                min_agreement: None,
                json: false,
            }),
            &c,
            false,
        )
        .await
        .err()
        .unwrap();
        assert_eq!(
            err.downcast_ref::<agentoven_core::ApiError>()
                .unwrap()
                .status,
            404
        );
    }
}
