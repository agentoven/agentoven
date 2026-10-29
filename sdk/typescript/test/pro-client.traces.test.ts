import { afterAll, beforeAll, beforeEach, describe, expect, it } from 'vitest';
import { ProClient } from '../src/pro-client.js';
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
  pro = new ProClient({ url: api.url, apiKey: 'ao_key', kitchen: 'alpha' });
});

describe('traces', () => {
  it('lists traces with the filters it is given and leaves out the empty ones', async () => {
    api.reply('GET', '/api/v1/traces?agent=support&environment=prod&view=v1&limit=5', []);
    await pro.listTraces({ agent: 'support', environment: 'prod', view: 'v1', limit: 5, status: '', tag: undefined });
    expect(api.last.path).toBe('/api/v1/traces?agent=support&environment=prod&view=v1&limit=5');
    api.reply('GET', '/api/v1/traces', [{ id: 't' }]);
    expect(await pro.listTraces()).toEqual([{ id: 't' }]);
  });

  it('reads the timeline, a page at a time, and the audit sequence', async () => {
    api.reply('GET', '/api/v1/traces/t%2F1/timeline', { schema_version: 1, spans: [], lanes: [] });
    expect((await pro.traceTimeline('t/1')).schema_version).toBe(1);
    api.reply('GET', '/api/v1/traces/t1/timeline?limit=100&cursor=100', { truncated: false });
    await pro.traceTimeline('t1', { limit: 100, cursor: '100' });
    expect(api.last.path).toBe('/api/v1/traces/t1/timeline?limit=100&cursor=100');
    api.reply('GET', '/api/v1/traces/t1/audit', { count: 2, coverage: { complete: true } });
    expect((await pro.traceAudit('t1')).count).toBe(2);
  });

  it('saves, lists, renames and deletes views', async () => {
    api.reply('POST', '/api/v1/trace-views', { id: 'v1', name: 'prod failures' }, 201);
    await pro.saveTraceView('prod failures', { status: 'failed', environment: 'prod' }, true);
    expect(api.last.body).toEqual({ name: 'prod failures', filter: { status: 'failed', environment: 'prod' }, shared: true });
    api.reply('GET', '/api/v1/trace-views', [{ id: 'v1' }]);
    expect(await pro.listTraceViews()).toEqual([{ id: 'v1' }]);
    api.reply('PUT', '/api/v1/trace-views/v1', { id: 'v1' });
    await pro.updateTraceView('v1', 'renamed', { tag: 'x' });
    expect(api.last.body).toEqual({ name: 'renamed', filter: { tag: 'x' }, shared: false });
    api.reply('DELETE', '/api/v1/trace-views/v1', {});
    await pro.deleteTraceView('v1');
    expect(api.last.method).toBe('DELETE');
    expect(api.last.headers['authorization']).toBe('Bearer ao_key');
  });
});
