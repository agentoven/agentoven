import { afterAll, beforeAll, beforeEach, describe, expect, it } from 'vitest';
import { AgentOvenAPIError, ProClient } from '../src/pro-client.js';
import { FakeControlPlane } from './fake-server.js';

const api = new FakeControlPlane();
let pro: ProClient;

beforeAll(async () => {
  await api.start();
});
afterAll(async () => {
  await api.stop();
});
beforeEach(() => {
  api.requests = [];
  pro = new ProClient({ url: api.url, apiKey: 'ao_key', kitchen: 'acme' });
});

const calls = () => api.requests.map((r) => `${r.method} ${r.path}`);
const schema = { type: 'object', properties: { amount: { type: 'number' } } };

describe('registerTool', () => {
  it('creates an http tool with POST when the name is new', async () => {
    api.reply('GET', '/api/v1/tools/refunds', { error: 'not found' }, 404);
    api.reply('POST', '/api/v1/tools', { id: 't1', name: 'refunds', transport: 'http' }, 201);
    const out = await pro.registerTool({ name: 'refunds', endpoint: 'https://pay.internal/refund', description: 'Pay a refund', schema });
    expect(out.id).toBe('t1');
    expect(calls()).toEqual(['GET /api/v1/tools/refunds', 'POST /api/v1/tools']);
    expect(api.last.body).toEqual({
      name: 'refunds',
      endpoint: 'https://pay.internal/refund',
      description: 'Pay a refund',
      transport: 'http',
      enabled: true,
      schema,
    });
    expect(api.last.headers['authorization']).toBe('Bearer ao_key');
    expect(api.last.headers['x-kitchen']).toBe('acme');
  });

  it('passes transport, capabilities, auth and enabled through', async () => {
    api.reply('GET', '/api/v1/tools/db', { error: 'not found' }, 404);
    api.reply('POST', '/api/v1/tools', { name: 'db' }, 201);
    await pro.registerTool({
      name: 'db',
      endpoint: 'http://db:9000/sse',
      transport: 'sse',
      capabilities: ['tool', 'notify'],
      auth_config: { type: 'bearer', token: 's3cret' },
      enabled: false,
    });
    expect(api.last.body).toMatchObject({
      transport: 'sse',
      capabilities: ['tool', 'notify'],
      auth_config: { type: 'bearer', token: 's3cret' },
      enabled: false,
    });
    expect(api.last.body).not.toHaveProperty('schema');
  });

  it('updates with PUT when the tool exists, and always sends enabled', async () => {
    // The server's update overwrites `enabled` from the body; leaving it out would switch the tool off.
    api.reply('GET', '/api/v1/tools/refunds', { name: 'refunds', enabled: true });
    api.reply('PUT', '/api/v1/tools/refunds', { name: 'refunds', endpoint: 'https://pay.internal/v2' });
    const out = await pro.registerTool({ name: 'refunds', endpoint: 'https://pay.internal/v2' });
    expect(out.endpoint).toBe('https://pay.internal/v2');
    expect(calls()).toEqual(['GET /api/v1/tools/refunds', 'PUT /api/v1/tools/refunds']);
    expect(api.last.body.enabled).toBe(true);
  });

  it('does not hide a failed lookup', async () => {
    api.reply('GET', '/api/v1/tools/refunds', { error: 'boom' }, 500);
    await expect(pro.registerTool({ name: 'refunds', endpoint: 'x' })).rejects.toMatchObject({ statusCode: 500 });
    expect(calls()).toEqual(['GET /api/v1/tools/refunds']);
  });

  it('surfaces a rejected transport', async () => {
    api.reply('GET', '/api/v1/tools/x', { error: 'not found' }, 404);
    api.reply('POST', '/api/v1/tools', { error: "Invalid transport: must be 'http', 'sse', or 'stdio'" }, 400);
    const err = await pro.registerTool({ name: 'x', endpoint: 'http://x', transport: 'grpc' }).catch((e) => e);
    expect(err).toBeInstanceOf(AgentOvenAPIError);
    expect(err.statusCode).toBe(400);
  });
});

describe('list, get and delete tools', () => {
  it('lists, gets and deletes, with the name escaped in the path', async () => {
    api.reply('GET', '/api/v1/tools', [{ name: 'refunds' }]);
    expect(await pro.listTools()).toEqual([{ name: 'refunds' }]);
    api.reply('GET', '/api/v1/tools/a%2Fb', { name: 'a/b' });
    expect((await pro.getTool('a/b')).name).toBe('a/b');
    api.reply('DELETE', '/api/v1/tools/refunds', {}, 204);
    await pro.deleteTool('refunds');
    expect(api.last.method).toBe('DELETE');
    expect(api.last.path).toBe('/api/v1/tools/refunds');
  });
});
