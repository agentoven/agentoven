import { createServer, type Server } from 'node:http';
import type { AddressInfo } from 'node:net';
import { afterEach, describe, expect, it } from 'vitest';
import { AgentOvenAPIError, ProClient } from '../src/pro-client.js';

let server: Server | undefined;
afterEach(() => new Promise<void>((done) => (server ? server.close(() => done()) : done())));

async function serve(handler: Parameters<typeof createServer>[1]): Promise<string> {
  server = createServer(handler);
  await new Promise<void>((resolve) => server!.listen(0, '127.0.0.1', resolve));
  return `http://127.0.0.1:${(server.address() as AddressInfo).port}`;
}

describe('transport (review 3, finding 15)', () => {
  it('a gateway error page is an AgentOvenAPIError that carries the text, not a TypeError', async () => {
    const url = await serve((_req, res) => {
      res.writeHead(502, { 'Content-Type': 'text/html' });
      res.end('<html><body>502 Bad Gateway</body></html>');
    });
    const err = await new ProClient({ url }).listGuardrails().catch((e) => e);
    expect(err).toBeInstanceOf(AgentOvenAPIError);
    expect(err.statusCode).toBe(502);
    expect(String(err.detail)).toContain('Bad Gateway');
  });

  it('a JSON error body is still parsed', async () => {
    const url = await serve((_req, res) => {
      res.writeHead(404, { 'Content-Type': 'application/json' });
      res.end('{"error":"no such guardrail"}');
    });
    const err = await new ProClient({ url }).listGuardrails().catch((e) => e);
    expect(err).toBeInstanceOf(AgentOvenAPIError);
    expect(err.detail).toEqual({ error: 'no such guardrail' });
  });

  it('a server that goes quiet ends in a timeout', async () => {
    const url = await serve(() => {
      /* accept and never answer */
    });
    const started = Date.now();
    await expect(new ProClient({ url, timeoutMs: 300 }).listGuardrails()).rejects.toThrow();
    expect(Date.now() - started).toBeLessThan(5000);
    // The clone for another kitchen keeps the limit.
    await expect(new ProClient({ url, timeoutMs: 300 }).withKitchen('k').listGuardrails()).rejects.toThrow();
  });

  it('an id cannot walk to another route', async () => {
    let seen = '';
    const url = await serve((req, res) => {
      seen = req.url ?? '';
      res.writeHead(200, { 'Content-Type': 'application/json' });
      res.end('{}');
    });
    await new ProClient({ url }).getUser('../../agents');
    expect(seen).toBe('/api/v1/users/..%2F..%2Fagents');
  });
});
