# ADR-0039: Document intelligence: in-process reading of Office and text files, OCR through the router, and grounded field extraction

**Status:** Accepted (built 2026-11-03)
**Scope:** OSS control plane (`pkg/documents`, `internal/router`, `internal/api/handlers`), SDKs (Python, TypeScript, Rust) and `agentoven documents`. Pro adds the permission, the audit names, the dashboard view and the trigger hand-over ([agentoven-pro ADR-0054](../../agentoven-pro/ADR/0054-document-intelligence.md)).
**Closes:** gap G3 of `AgentOven-UseCase-Gap-Analysis.md` section 3.2, in part (see "Not done").

## Context

Before this change the shipped path read text and the text layer of PDFs and nothing else (`pkg/documents/pdf.go`, `internal/router/media.go`). A Word or Excel attachment was refused (`"openai does not accept \"file\" content parts"`), HTML and CSV could not be uploaded at all (`blobMimeAllowed` took images, audio, video and PDF), a scanned PDF was refused with "may be a scan", tables were lost, and nothing said where an extracted value came from.

The knowledge engine (`agentoven-knowledge`, [K-003](../../agentoven-knowledge/ADR/K-003-documents-parsers-chunking.md)) already has parsers: text, Markdown, HTML, DOCX, PDF text into an element model with heading paths, and a Docling driver for layout, tables and OCR. They are built for ingestion: chunking, retrieval, citations, indexes. They are not in the OSS tree, and Pro's integration branch (`feat/knowledge-engine-integration`, [Pro ADR-0036](../../agentoven-pro/ADR/0036-knowledge-engine-agentoven-knowledge.md)) is unmerged and unpushed. XLSX is not among them.

## Decision

### 1. A small reader in OSS `pkg/documents`, not a copy of the engine's parsers

The two jobs differ. The engine turns a corpus into chunks that are retrieved later. This layer turns **one attachment into the text a model reads in this call**, quickly, in-process, with the structure a model needs (headings, tables as markdown with sheet, range and row numbers, `[Page N]` labels) and with every limit that makes reading a stranger's file safe. It needs no database, no job queue and no sidecar. So it is a separate, small package with one dependency beyond the standard library (`golang.org/x/net/html` tokenizer, already in the module graph) and the PDF library already used.

What it reads (`documents.Extract`, `Detect`): DOCX, XLSX, HTML, CSV/TSV, Markdown, plain text, RTF, PDF (per page). The engine remains the answer for indexing; when Pro merges it, the engine's Docling driver is the better reader for table-heavy PDFs, and this package is the in-call fallback that works without a sidecar. Nothing here is imported from the engine and nothing in the engine changed.

### 2. Safety is in the reader, once

- **Archives** (`ooxml.go`): entry count bound (2000), duplicate entry names refused (they show different readers different content), every part read through a reader that fails at the lesser of 64 MB and 200 times the part's compressed size (header claims are not trusted; a lying header is tested), parts found by fixed name or through the package's relationships, never from a path in the archive, and an archive inside the archive is never opened (nesting depth is one by construction; an embedded workbook is ignored).
- **XML**: a document type declaration is refused outright, so no entity is ever defined and none can name a file or URL (Go's `encoding/xml` resolves none; the refusal makes that a tested property). Element depth is bounded (128). Parsing is streaming.
- **Sheets**: only cells with a value are kept (a sheet claiming a million rows costs the rows it fills), columns bound (256), cells bound (500,000), sheets bound (64), hidden sheets are skipped and the result says so.
- **What a person cannot see, a model does not see**: hidden DOCX runs, deleted tracked changes, the duplicate fallback of a text box, hidden HTML (`hidden`, `display:none`, `visibility:hidden`), scripts, styles and hidden sheets are dropped, and so are zero-width characters, bidirectional overrides and the Unicode tag block, the usual ways to plant instructions. Every file's text is wrapped with a line saying it is data, not instructions.
- **Text budget**: one document contributes at most 200,000 bytes (cut and flagged `truncated`, never an error); all documents of one model call at most 600,000.
- **PDF**: Flate streams are inflated to nowhere within a budget (128 MB each, 512 MB in all) before the PDF library, which decompresses into memory without a limit, sees the file; the library runs on its own goroutine under the caller's context and any panic becomes an error.
- Fuzzed: `go test -fuzz` targets for the DOCX body, XLSX sheet, HTML, RTF, delimited text and PDF readers (20 seconds each at the time of writing, millions of executions, no finding).

### 3. Converted before the provider call, at the router

`ModelRouter.withDocuments` (`internal/router/documents.go`) replaces every `file` part whose type is one of those formats with a text part, in `callProvider` and in the streaming path, after the response-cache lookup and before the spend gate reserves and the driver maps the request. That is the capability gate: no provider takes these formats natively, so they always become text, for every driver, including those Pro registers. PDFs and images keep their native path; a PDF sent to a model without PDF input now falls back to text **with page labels**. The caller's request is not modified, the cache is keyed on the request as written (a blob ref names the same bytes every time), and a file that cannot be read is a `MediaError` (4xx) naming the file, never a provider fault.

`POST /api/v1/blobs` accepts these types, settling the type by the file name when the declared one says little (a docx arrives as `application/zip` from many clients).

### 4. API

| Route | What | Model call |
|---|---|---|
| `POST /api/v1/documents/extract` | A `blob_ref` or an upload (multipart or raw, not stored) read into `text` and typed `blocks`; `needs_ocr` and `empty_pages` for a scan | never |
| `POST /api/v1/documents/ocr` | `blob_ref`, `provider`, `model?`, `max_pages?`, `verify?` | one call per page |
| `POST /api/v1/documents/fields` | `blob_ref` or `text`, JSON Schema, `provider`, `ocr?` | one call (plus OCR) |

All three are scoped to the caller's kitchen (a blob ref of another kitchen is a 404, identical to a missing one), size-limited (JSON bodies 1 MB, uploads `AGENTOVEN_BLOB_MAX_BYTES`), bounded in concurrency (a reading slot is taken before the file is read; none free is a 429 with `Retry-After`) and bounded in time (30 s per read).

### 5. OCR is a routed call, one page at a time

`documents.OCR` sends each page that has no text layer, alone, to the vision model of the **provider the caller names**, as an ordinary `Router.Route` call pinned to that provider: the spend gate, rate limits, the response cache, circuit breakers and the trace all apply, there is no separate network path, and a document never goes to a provider the caller did not choose. Calls carry `purpose=document_ocr` and the agent name `document-ocr`; pictures are moved into the blob store first (`ExternalizeMedia`) so the call record holds a reference. Pages that have a text layer are used as they are and cost nothing. The page count is bounded (default 10, at most 200); the first failure stops the run and the result lists what was read and says it is `incomplete`.

Page pictures: a scanner (or "print a photo to PDF") stores each page as one JPEG, and a JPEG stream is a JPEG file, so it is handed to the model unchanged, with no rasteriser. Streams are found by scanning the file and matched to pages by width, height and length as the page's resources declare them. A scan stored any other way (JBIG2, CCITT, raw or Flate pixels, JPEG 2000) is reported per page, not guessed at.

### 6. Confidence is a statement about the process, not a number

A model's own statement of how sure it is carries no usable information, so none is requested or reported. Every OCR page has `confidence`: `extracted` (text layer, no model), `unverified` (one model, nothing checked: the default and the only honest value without a second pass), `agreed` or `disputed` (a second transcription by a **different** provider or model, compared by word-level edit distance against a threshold, default 0.97). A second pass by the same model is refused (at temperature 0 it repeats itself and would prove nothing). `agreed` is evidence, not proof: two models can make the same mistake. The result carries the lowest confidence of its pages, and the response says so in words.

### 7. Fields carry their source and are checked against it

`documents.ExtractFields` asks for each property of a flat JSON Schema (strings, numbers, integers, booleans, enums, arrays of those; nested objects are refused with the reason) as `{value, page, quote}` through a strict structured-output request, then **checks every quote against the document text itself** (case, whitespace, typographic quotes and dashes are forgiven; a changed digit is not). Each field is `grounded` (quote found on the page claimed or, with flag `page_corrected`, elsewhere, and the value readable from the quote: a number must equal a number in it, so 1250.5 matches "$1,250.50"), `ungrounded` (flags: `quote_missing`, `quote_not_found`, `value_not_in_quote`, `wrong_type`, `not_in_enum`; the value is returned, never dropped or silently kept) or `absent`. `all_grounded` is false when any field is ungrounded or a required field is absent. Grounded means the text supports the value, not that the text is right: a misread scan stays misread, which is why a field read from an OCR transcription carries the transcription's confidence in the response.

## Consequences

- An attached DOCX, XLSX, HTML or CSV works on every provider and every driver; so does a text-layer PDF on a model without PDF input.
- Reading a file costs CPU on the control plane, bounded as above. Reading runs on the request goroutine (or the router's), not in a sandbox.
- A new dependency edge: `golang.org/x/net` moves from indirect to direct in the OSS and Pro `go.mod`.
- The scan path depends on a heuristic (file scan plus declared attributes) that is tested on generated PDFs and not on a corpus of real scanner output.

## Not done

- **Process isolation of the PDF library.** It cannot be interrupted; a file that makes it loop forever leaves a goroutine behind (the request still ends at its deadline), and the inflation budget does not cover every way it can allocate. Running it in a child process with a memory limit is the real answer.
- **PDF tables, columns and layout.** Text comes in reading order. Docling (engine K-003) is the answer; wiring it in here is not done.
- **Scans that are not JPEG**, and rasterising vector pages for OCR.
- **DOCX headers, footers, footnotes, comments, images and ordered-list numbering; XLSX charts, comments, merged ranges and defined names; legacy `.doc` and `.xls`; password-protected files.**
- **Automatic OCR inside an agent run.** A scanned PDF sent to a model without PDF input is still refused, now with a message that points at `/documents/ocr`; running OCR implicitly would spend money nobody asked for.
- **Token accounting for converted documents in context trimming**: a document part still counts as 1000 tokens in `ctxwindow` before conversion.
- **A calibrated confidence.** Not attempted; see section 6.
- **Per-field extraction across several documents, and nested schemas.**
- Memoising a conversion across the retries of one call (each attempt reads the file again).
