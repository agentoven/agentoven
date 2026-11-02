import { afterAll, beforeAll, beforeEach, describe, expect, it } from 'vitest';
import { createRequire } from 'node:module';
import { FakeControlPlane } from './fake-server.js';

// The native (napi-rs) client is a build artifact: `npm run build` produces it.
// Without it this file has nothing to test, so it skips rather than fails.
type Native = typeof import('../index.js');
let native: Native | undefined;
try {
  native = createRequire(import.meta.url)('../index.js') as Native;
} catch {
  native = undefined;
}

const api = new FakeControlPlane();
beforeAll(async () => {
  await api.start();
});
afterAll(async () => {
  await api.stop();
});
beforeEach(() => {
  api.requests = [];
});

const stored = {
  id: 'a1',
  name: 'refund-bot',
  version: '1.0.0',
  description: 'Pays refunds',
  framework: 'custom',
  status: 'draft',
  created_at: '2026-11-01T00:00:00Z',
  updated_at: '2026-11-01T00:00:00Z',
};
const calls = () => api.requests.map((r) => `${r.method} ${r.path}`);

describe.skipIf(!native)('native registerAgent', () => {
  const client = () => new native!.AgentOvenClient(api.url, 'ao_key', 'acme');
  const agent = () => native!.createAgent('refund-bot', 'Pays refunds', null, '1.0.0');

  it('creates with POST when the agent does not exist', async () => {
    api.reply('GET', '/api/v1/agents/refund-bot', { error: 'not found' }, 404);
    api.reply('POST', '/api/v1/agents', stored, 201);
    const out = await client().registerAgent(agent());
    expect(out).toContain("'refund-bot' registered");
    expect(calls()).toEqual(['GET /api/v1/agents/refund-bot', 'POST /api/v1/agents']);
    expect(api.last.body).toMatchObject({ name: 'refund-bot', description: 'Pays refunds', version: '1.0.0' });
    expect(api.last.headers['authorization']).toBe('Bearer ao_key');
    expect(api.last.headers['x-kitchen']).toBe('acme');
  });

  it('updates with PUT when the agent exists', async () => {
    api.reply('GET', '/api/v1/agents/refund-bot', stored);
    api.reply('PUT', '/api/v1/agents/refund-bot', stored);
    await client().registerAgent(agent());
    expect(calls()).toEqual(['GET /api/v1/agents/refund-bot', 'PUT /api/v1/agents/refund-bot']);
  });

  it('rejects, and changes nothing, when the lookup fails', async () => {
    api.reply('GET', '/api/v1/agents/refund-bot', { error: 'boom' }, 500);
    await expect(client().registerAgent(agent())).rejects.toThrow(/look up agent 'refund-bot'|500/);
    expect(calls()).toEqual(['GET /api/v1/agents/refund-bot']);
  });
});
