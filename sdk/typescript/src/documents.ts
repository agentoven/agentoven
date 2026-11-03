/**
 * Read what a file says, transcribe scans, and pull fields out with the quote they came from.
 *
 * ```ts
 * const docs = new Documents({ url: 'http://localhost:8080', apiKey: '...', kitchen: 'claims' });
 * const doc = await docs.extract(bytes, { name: 'claim.docx' });   // read in-process, no model
 * console.log(doc.text);                                           // tables as markdown; [Sheet: ...] / [Page N] labels
 * const stored = await blobs.upload(bytes, { name: 'scan.pdf' });
 * const scan = await docs.ocr(stored.ref, { provider: 'vision' }); // page by page, through your provider
 * scan.confidence;                                                 // 'unverified' unless `verify` asked for a second model
 * await docs.extractFields(stored.ref, schema, { provider: 'vision' }); // every field comes back with its quote
 * ```
 *
 * `extract` reads Word, Excel, HTML, CSV/TSV, Markdown, RTF, plain text and a PDF's text layer. A page with
 * no text layer is listed in `empty_pages` and `needs_ocr` is true: it is not returned as blank. `ocr` and
 * `extractFields` send the document to the one provider you name, through the control plane's router, so
 * budgets, rate limits and traces apply. `confidence` says what was checked, not a probability; a field
 * whose `status` is `ungrounded` has a quote that is not in the document, so its value is not to be used unseen.
 */

import { guessMimeType, sniffMimeType } from './media.js';
import { AgentOvenAPIError } from './pro-client.js';

export interface DocumentsOptions {
  url: string;
  apiKey?: string;
  kitchen?: string;
  /** How long one request may take, in milliseconds (default 120 000). OCR reads a page per model call: raise it for long scans. */
  timeoutMs?: number;
}

export interface DocumentBlock {
  kind: 'heading' | 'paragraph' | 'list_item' | 'table';
  text?: string;
  level?: number;
  page?: number;
  sheet?: string;
  table?: { range?: string; columns?: string[]; row_numbers?: number[]; rows: string[][]; truncated?: boolean };
}

export interface ExtractedDocument {
  blob_ref?: string;
  name?: string;
  mime_type: string;
  size: number;
  format: string;
  /** The text a model would be shown: headings, lists and tables as markdown, with sheet and page labels. */
  text: string;
  blocks: DocumentBlock[];
  pages?: number;
  sheets?: string[];
  warnings?: string[];
  truncated?: boolean;
  /** Some pages are images of text this reader cannot read: run `ocr`. */
  needs_ocr?: boolean;
  empty_pages?: number[];
}

export type OcrConfidence = 'extracted' | 'unverified' | 'agreed' | 'disputed';

export interface OcrResult {
  blob_ref: string;
  name?: string;
  format: string;
  pages: { page: number; text: string; source: 'text_layer' | 'ocr'; confidence: OcrConfidence; agreement?: number; error?: string }[];
  /** The document's text with `[Page N]` labels. */
  text: string;
  /** The weakest page's. `unverified` is one model's reading with no second pass. */
  confidence: OcrConfidence;
  confidence_note: string;
  /** A page was not read (page bound, failure, a scan this reader cannot extract); `warnings` say which. */
  incomplete: boolean;
  warnings?: string[];
  provider: string;
  model?: string;
  usage: { input_tokens: number; output_tokens: number; total_tokens: number; estimated_cost_usd: number };
}

export interface OcrOptions {
  provider: string;
  model?: string;
  /** Pages read without a text layer (default 10). */
  maxPages?: number;
  /** Read every page a second time with a different provider or model. */
  verify?: { provider?: string; model?: string; minAgreement?: number };
}

export type FieldStatus = 'grounded' | 'ungrounded' | 'absent';

export interface ExtractedField {
  name: string;
  value: unknown;
  status: FieldStatus;
  page?: number;
  quote?: string;
  /** Why a field is not grounded: quote_missing, quote_not_found, value_not_in_quote, wrong_type, not_in_enum, ... */
  flags?: string[];
}

export interface FieldsResult {
  fields: ExtractedField[];
  provider: string;
  model?: string;
  usage: OcrResult['usage'];
  /** True only when every field the schema asks for is grounded. */
  all_grounded: boolean;
  warnings?: string[];
}

export interface FieldsOptions {
  provider: string;
  model?: string;
  instructions?: string;
  /** Read a scan first: `{}` uses the same provider, or name another. */
  ocr?: { provider?: string; model?: string; maxPages?: number; verify?: OcrOptions['verify'] };
}

export interface ExtractOptions {
  /** For bytes: the file name (its extension tells the type) and an explicit type. */
  name?: string;
  mimeType?: string;
  /** For a stored file (`blob_ref`): cut the text at this many bytes. */
  maxTextBytes?: number;
}

const verifyBody = (v?: OcrOptions['verify']) =>
  v && { provider: v.provider, model: v.model, min_agreement: v.minAgreement };

export class Documents {
  private readonly baseUrl: string;
  private readonly apiKey?: string;
  private readonly kitchen: string;
  private readonly timeoutMs: number;

  constructor(opts: DocumentsOptions) {
    this.baseUrl = opts.url.replace(/\/+$/, '');
    this.apiKey = opts.apiKey;
    this.kitchen = opts.kitchen ?? 'default';
    this.timeoutMs = opts.timeoutMs ?? 120_000;
  }

  /**
   * Read a file into text and structure. A string is the `blob_ref` of a stored file (nothing is uploaded);
   * bytes are sent for this call only and not stored.
   */
  async extract(source: string | Uint8Array | ArrayBuffer, opts: ExtractOptions = {}): Promise<ExtractedDocument> {
    if (typeof source === 'string') {
      return this.json('/api/v1/documents/extract', { blob_ref: source, ...(opts.maxTextBytes ? { max_text_bytes: opts.maxTextBytes } : {}) });
    }
    if (opts.maxTextBytes) throw new Error('maxTextBytes applies to a stored file (blob_ref); an upload is cut at the server default');
    const bytes = source instanceof Uint8Array ? source : new Uint8Array(source);
    const mime = opts.mimeType ?? (opts.name ? guessMimeType(opts.name) : undefined) ?? sniffMimeType(bytes);
    if (!mime) throw new Error('cannot tell the media type of these bytes; pass mimeType or name');
    const q = opts.name ? `?name=${encodeURIComponent(opts.name)}` : '';
    const res = await this.send(`/api/v1/documents/extract${q}`, bytes, mime);
    return (await res.json()) as ExtractedDocument;
  }

  /** Transcribe an image or a scanned PDF, one page per model call, with the provider you name. */
  ocr(blobRef: string, opts: OcrOptions): Promise<OcrResult> {
    return this.json('/api/v1/documents/ocr', {
      blob_ref: blobRef,
      provider: opts.provider,
      ...(opts.model ? { model: opts.model } : {}),
      ...(opts.maxPages ? { max_pages: opts.maxPages } : {}),
      ...(opts.verify ? { verify: verifyBody(opts.verify) } : {}),
    });
  }

  /** Read the properties of a flat JSON Schema out of a stored document (a `blob_ref`) or out of `{ text }`. */
  extractFields(source: string | { text: string }, schema: Record<string, unknown>, opts: FieldsOptions): Promise<FieldsResult> {
    return this.json('/api/v1/documents/fields', {
      ...(typeof source === 'string' ? { blob_ref: source } : { text: source.text }),
      schema,
      provider: opts.provider,
      ...(opts.model ? { model: opts.model } : {}),
      ...(opts.instructions ? { instructions: opts.instructions } : {}),
      ...(opts.ocr ? { ocr: { provider: opts.ocr.provider, model: opts.ocr.model, max_pages: opts.ocr.maxPages, ...(opts.ocr.verify ? { verify: verifyBody(opts.ocr.verify) } : {}) } } : {}),
    });
  }

  private async json<T>(path: string, body: unknown): Promise<T> {
    const res = await this.send(path, JSON.stringify(body), 'application/json');
    return (await res.json()) as T;
  }

  private async send(path: string, body: string | Uint8Array, contentType: string): Promise<Response> {
    const h: Record<string, string> = { 'X-Kitchen': this.kitchen, 'X-Kitchen-Id': this.kitchen, 'Content-Type': contentType };
    if (this.apiKey) h['Authorization'] = `Bearer ${this.apiKey}`;
    const res = await fetch(`${this.baseUrl}${path}`, { method: 'POST', headers: h, body: body as BodyInit, signal: AbortSignal.timeout(this.timeoutMs) });
    if (!res.ok) {
      const text = await res.text();
      let detail: unknown;
      try {
        detail = JSON.parse(text);
      } catch {
        detail = text;
      }
      throw new AgentOvenAPIError(res.status, detail);
    }
    return res;
  }
}
