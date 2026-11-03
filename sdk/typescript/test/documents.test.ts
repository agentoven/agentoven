import { createServer, type Server } from 'node:http';
import { afterAll, beforeAll, beforeEach, describe, expect, it } from 'vitest';
import { Documents } from '../src/documents.js';
import { AgentOvenAPIError } from '../src/pro-client.js';
import { guessMimeType } from '../src/media.js';

const REF = 'ab'.repeat(32);
const DOCX = 'application/vnd.openxmlformats-officedocument.wordprocessingml.document';

interface Seen {
  method: string;
  url: string;
  headers: Record<string, string | string[] | undefined>;
  body: Buffer;
}
let server: Server;
let url = '';
let seen: Seen[] = [];
let status = 200;
let reply: unknown = { format: 'docx', text: 'hello', blocks: [] };

const last = () => seen[seen.length - 1];
const lastJson = () => JSON.parse(last().body.toString());

beforeAll(async () => {
  server = createServer((req, res) => {
    const chunks: Buffer[] = [];
    req.on('data', (c) => chunks.push(c));
    req.on('end', () => {
      seen.push({ method: req.method!, url: req.url!, headers: req.headers, body: Buffer.concat(chunks) });
      res.writeHead(status, { 'Content-Type': 'application/json' });
      res.end(JSON.stringify(reply));
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
  status = 200;
  reply = { format: 'docx', text: 'hello', blocks: [] };
});

const docs = () => new Documents({ url, apiKey: 'k', kitchen: 'claims' });

describe('Documents', () => {
  it('extract of a stored file sends only its reference', async () => {
    const out = await docs().extract(REF, { maxTextBytes: 5000 });
    expect(last().method).toBe('POST');
    expect(last().url).toBe('/api/v1/documents/extract');
    expect(last().headers['content-type']).toBe('application/json');
    expect(last().headers['x-kitchen']).toBe('claims');
    expect(last().headers['authorization']).toBe('Bearer k');
    expect(lastJson()).toEqual({ blob_ref: REF, max_text_bytes: 5000 });
    expect(out.text).toBe('hello');
  });

  it('extract of bytes uploads them for this call only, typed by name or content', async () => {
    const bytes = new Uint8Array([0x50, 0x4b, 3, 4]);
    await docs().extract(bytes, { name: 'claim.docx' });
    expect(last().url).toBe('/api/v1/documents/extract?name=claim.docx');
    expect(last().headers['content-type']).toBe(DOCX);
    expect(Array.from(last().body)).toEqual(Array.from(bytes));
    await docs().extract(new TextEncoder().encode('a,b\n1,2\n'), { name: 'lines.csv' });
    expect(last().headers['content-type']).toBe('text/csv');
    await docs().extract(bytes, { mimeType: 'application/pdf' });
    expect(last().url).toBe('/api/v1/documents/extract');
    expect(last().headers['content-type']).toBe('application/pdf');
  });

  it('extract refuses what it cannot send', async () => {
    await expect(docs().extract(new Uint8Array([1, 2, 3]))).rejects.toThrow(/media type/);
    await expect(docs().extract(new Uint8Array([1]), { name: 'a.txt', maxTextBytes: 5 })).rejects.toThrow(/maxTextBytes/);
    expect(seen).toHaveLength(0);
  });

  it('ocr names the provider and the second pass', async () => {
    reply = { text: '[Page 1]\nx', confidence: 'unverified' };
    const out = await docs().ocr(REF, { provider: 'vision', model: 'gpt-4o', maxPages: 3, verify: { provider: 'vision', model: 'other', minAgreement: 0.9 } });
    expect(last().url).toBe('/api/v1/documents/ocr');
    expect(lastJson()).toEqual({ blob_ref: REF, provider: 'vision', model: 'gpt-4o', max_pages: 3, verify: { provider: 'vision', model: 'other', min_agreement: 0.9 } });
    expect(out.confidence).toBe('unverified');
    await docs().ocr(REF, { provider: 'vision' });
    expect(lastJson()).toEqual({ blob_ref: REF, provider: 'vision' });
  });

  it('extractFields from a blob or from text', async () => {
    const schema = { type: 'object', properties: { total: { type: 'number' } } };
    reply = { fields: [{ name: 'total', status: 'grounded' }], all_grounded: true };
    const out = await docs().extractFields(REF, schema, { provider: 'vision', model: 'm', instructions: 'USD', ocr: {} });
    expect(last().url).toBe('/api/v1/documents/fields');
    expect(lastJson()).toEqual({ blob_ref: REF, schema, provider: 'vision', model: 'm', instructions: 'USD', ocr: {} });
    expect(out.all_grounded).toBe(true);
    await docs().extractFields({ text: 'Total 5' }, schema, { provider: 'vision' });
    expect(lastJson()).toEqual({ text: 'Total 5', schema, provider: 'vision' });
  });

  it('raises the API error with the status', async () => {
    status = 422;
    reply = { error: 'the document has no text layer' };
    const err = await docs().extract(REF).catch((e) => e);
    expect(err).toBeInstanceOf(AgentOvenAPIError);
    expect((err as AgentOvenAPIError).statusCode).toBe(422);
    status = 404;
    reply = { error: 'blob not found' };
    expect(((await docs().ocr(REF, { provider: 'v' }).catch((e) => e)) as AgentOvenAPIError).statusCode).toBe(404);
  });

  it('knows the document extensions', () => {
    expect(guessMimeType('claim.DOCX')).toBe(DOCX);
    expect(guessMimeType('c.xlsx')).toMatch(/spreadsheetml\.sheet$/);
    expect(guessMimeType('a.tsv')).toBe('text/tab-separated-values');
    expect(guessMimeType('a.md')).toBe('text/markdown');
  });
});
