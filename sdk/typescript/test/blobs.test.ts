import { createServer, type Server } from 'node:http';
import { afterAll, beforeAll, beforeEach, describe, expect, it } from 'vitest';
import { blobPart, Blobs } from '../src/blobs.js';
import { AgentOvenAPIError } from '../src/pro-client.js';
import type { ContentPart } from '../src/media.js';

const PNG = new Uint8Array([0x89, 0x50, 0x4e, 0x47, 0x0d, 0x0a, 0x1a, 0x0a, 0, 0, 0, 0]);
const REF = 'ab'.repeat(32);

interface Seen {
  method: string;
  url: string;
  headers: Record<string, string | string[] | undefined>;
  body: Buffer;
}
let server: Server;
let url = '';
let seen: Seen[] = [];
let status = 201;
let reply: Buffer | undefined;

beforeAll(async () => {
  server = createServer((req, res) => {
    const chunks: Buffer[] = [];
    req.on('data', (c) => chunks.push(c));
    req.on('end', () => {
      const body = Buffer.concat(chunks);
      seen.push({ method: req.method!, url: req.url!, headers: req.headers, body });
      const out =
        reply ??
        (req.method === 'POST'
          ? Buffer.from(JSON.stringify({ blob_ref: REF, mime_type: req.headers['content-type'], name: 't.png', size: body.length }))
          : req.method === 'GET'
            ? Buffer.from(PNG)
            : Buffer.alloc(0));
      res.writeHead(req.method === 'POST' ? status : req.method === 'DELETE' ? 204 : 200);
      res.end(out);
    });
  });
  await new Promise<void>((r) => server.listen(0, '127.0.0.1', r));
  url = `http://127.0.0.1:${(server.address() as { port: number }).port}`;
});
afterAll(async () => {
  await new Promise<void>((r) => server.close(() => r()));
});
beforeEach(() => {
  seen = [];
  status = 201;
  reply = undefined;
});

describe('Blobs', () => {
  it('uploads raw bytes with the sniffed type and returns an attachment part', async () => {
    const blobs = new Blobs({ url, apiKey: 'k', kitchen: 'payments' });
    const b = await blobs.upload(PNG, { name: 't.png' });
    const req = seen[0];
    expect(req.method).toBe('POST');
    expect(req.url).toBe('/api/v1/blobs?name=t.png');
    expect(req.headers['content-type']).toBe('image/png');
    expect(req.headers['authorization']).toBe('Bearer k');
    expect(req.headers['x-kitchen']).toBe('payments');
    expect(Array.from(req.body)).toEqual(Array.from(PNG));
    expect(b.ref).toBe(REF);
    expect(b.size).toBe(PNG.length);
    // Assignable wherever attachments go.
    const asAttachment: ContentPart = b.part;
    expect(asAttachment).toEqual({ type: 'image', media: { mime_type: 'image/png', blob_ref: REF, name: 't.png' } });
  });

  it('needs a media type it can work out', async () => {
    await expect(new Blobs({ url }).upload(new Uint8Array([1, 2, 3]))).rejects.toThrow(/media type/);
    await new Blobs({ url }).upload(new Uint8Array([1, 2, 3]), { mimeType: 'audio/flac' });
    expect(seen[0].headers['content-type']).toBe('audio/flac');
  });

  it('downloads and deletes', async () => {
    const blobs = new Blobs({ url });
    expect(Array.from(await blobs.download(REF))).toEqual(Array.from(PNG));
    expect(seen[0].url).toBe(`/api/v1/blobs/${REF}`);
    await blobs.delete(REF);
    expect(seen[1].method).toBe('DELETE');
  });

  it('raises the API error with the status', async () => {
    status = 413;
    reply = Buffer.from('{"error":"the upload is larger than the limit"}');
    const err = await new Blobs({ url }).upload(PNG).catch((e) => e);
    expect(err).toBeInstanceOf(AgentOvenAPIError);
    expect((err as AgentOvenAPIError).statusCode).toBe(413);
  });

  it('blobPart types a stored file by its media type', () => {
    expect(blobPart(REF, 'application/pdf').type).toBe('file');
    expect(blobPart(REF, 'audio/wav').type).toBe('audio');
    expect(blobPart(REF, 'video/mp4').media.blob_ref).toBe(REF);
  });
});
