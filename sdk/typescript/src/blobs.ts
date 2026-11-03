/**
 * Upload a file once, then name it in messages by `blob_ref`.
 *
 * ```ts
 * const blobs = new Blobs({ url: 'http://localhost:8080', apiKey: '...' });
 * const contract = await blobs.upload(await readFile('contract.pdf'), { name: 'contract.pdf' });
 * await pro.sendSessionMessage('legal', session.id, { content: 'Summarise', attachments: [contract.part] });
 * await pro.invoke('legal', { message: 'Summarise', attachments: [contract.part] });
 * ```
 *
 * A blob belongs to the kitchen that uploaded it: another kitchen gets a 404 for the ref, so a
 * ref is not a credential. The server's limit is `AGENTOVEN_BLOB_MAX_BYTES` (25 MB by default,
 * 413 when exceeded) and unused blobs expire after `AGENTOVEN_BLOB_TTL_DAYS` (7 by default).
 */

import { guessMimeType, sniffMimeType, type ContentPartType } from './media.js';
import { AgentOvenAPIError } from './pro-client.js';

export interface BlobsOptions {
  url: string;
  apiKey?: string;
  kitchen?: string;
  /** How long one request may take, in milliseconds (default 120 000). */
  timeoutMs?: number;
}

export interface UploadOptions {
  /** Overrides inference. Needed for bytes of a format the SDK cannot recognise. */
  mimeType?: string;
  /** The original file name, shown in downloads and sent to providers that want one. */
  name?: string;
}

/** A content part that names a stored blob. Assignable to the `ContentPart` that attachments take. */
export interface BlobPart {
  type: ContentPartType;
  media: { mime_type: string; blob_ref: string; name?: string };
}

/** A file stored on the control plane. */
export interface Blob {
  ref: string;
  mimeType: string;
  name?: string;
  size: number;
  /** The `content_parts` entry that refers to the stored file; pass it as an attachment. */
  part: BlobPart;
}

function partType(mime: string): ContentPartType {
  if (mime.startsWith('image/')) return 'image';
  if (mime.startsWith('audio/')) return 'audio';
  if (mime.startsWith('video/')) return 'video';
  return 'file';
}

/** The attachment for a stored blob (no upload). */
export function blobPart(ref: string, mimeType: string, name?: string): BlobPart {
  return { type: partType(mimeType), media: { mime_type: mimeType, blob_ref: ref, ...(name ? { name } : {}) } };
}

export class Blobs {
  private readonly baseUrl: string;
  private readonly apiKey?: string;
  private readonly kitchen: string;
  private readonly timeoutMs: number;

  constructor(opts: BlobsOptions) {
    this.timeoutMs = opts.timeoutMs ?? 120_000;
    this.baseUrl = opts.url.replace(/\/+$/, '');
    this.apiKey = opts.apiKey;
    this.kitchen = opts.kitchen ?? 'default';
  }

  async upload(data: Uint8Array | ArrayBuffer, opts: UploadOptions = {}): Promise<Blob> {
    const bytes = data instanceof Uint8Array ? data : new Uint8Array(data);
    const mime = opts.mimeType ?? (opts.name ? guessMimeType(opts.name) : undefined) ?? sniffMimeType(bytes);
    if (!mime) throw new Error('cannot tell the media type of these bytes; pass mimeType');
    const q = opts.name ? `?name=${encodeURIComponent(opts.name)}` : '';
    const res = await this.send('POST', `/api/v1/blobs${q}`, bytes, { 'Content-Type': mime });
    const body = (await res.json()) as { blob_ref: string; mime_type: string; name?: string; size?: number };
    const name = body.name || undefined;
    return { ref: body.blob_ref, mimeType: body.mime_type, name, size: body.size ?? 0, part: blobPart(body.blob_ref, body.mime_type, name) };
  }

  /** The stored bytes (only for the kitchen that owns them). */
  async download(ref: string): Promise<Uint8Array> {
    const res = await this.send('GET', `/api/v1/blobs/${encodeURIComponent(ref)}`);
    return new Uint8Array(await res.arrayBuffer());
  }

  /** Drop this kitchen's copy. */
  async delete(ref: string): Promise<void> {
    await this.send('DELETE', `/api/v1/blobs/${encodeURIComponent(ref)}`);
  }

  private async send(method: string, path: string, body?: Uint8Array, headers: Record<string, string> = {}): Promise<Response> {
    const h: Record<string, string> = { 'X-Kitchen': this.kitchen, 'X-Kitchen-Id': this.kitchen, ...headers };
    if (this.apiKey) h['Authorization'] = `Bearer ${this.apiKey}`;
    const res = await fetch(`${this.baseUrl}${path}`, { method, headers: h, body: body as BodyInit | undefined, signal: AbortSignal.timeout(this.timeoutMs) });
    if (!res.ok) {
      let detail: unknown;
      const text = await res.text();
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
