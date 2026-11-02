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
  pro = new ProClient({ url: api.url, apiKey: 'ao_key', kitchen: 'acme' });
});

describe('tool packs', () => {
  it('unwrap the catalog and the installs', async () => {
    api.reply('GET', '/api/v1/tool-packs/catalog', { packs: [{ name: 'postgres-readonly' }] });
    api.reply('GET', '/api/v1/tool-packs', { installs: [{ name: 'warehouse', status: 'active' }] });
    expect((await pro.listToolPackCatalog())[0].name).toBe('postgres-readonly');
    expect((await pro.listToolPacks())[0].status).toBe('active');
    expect(api.last.headers['x-kitchen']).toBe('acme');
  });

  it('install with references only, upgrade, rebind, check and uninstall', async () => {
    api.reply('POST', '/api/v1/tool-packs', { name: 'warehouse' }, 201);
    await pro.installToolPack({ pack: 'postgres-readonly', secrets: { dsn: 'vault://wh' }, tools: { query: { enabled: true } } });
    expect(api.last.body).toEqual({ pack: 'postgres-readonly', secrets: { dsn: 'vault://wh' }, tools: { query: { enabled: true } } });
    api.reply('POST', '/api/v1/tool-packs/wh/upgrade', { version: '1.1.0' });
    await pro.upgradeToolPack('wh');
    expect(api.last.body).toEqual({});
    api.reply('PUT', '/api/v1/tool-packs/wh/secrets', { name: 'wh' });
    await pro.rebindToolPackSecrets('wh', { dsn: 'vault://other' });
    expect(api.last.body).toEqual({ secrets: { dsn: 'vault://other' } });
    api.reply('POST', '/api/v1/tool-packs/wh/check', { ok: true });
    expect(await pro.checkToolPack('wh')).toEqual({ ok: true });
    api.reply('DELETE', '/api/v1/tool-packs/wh', {}, 204);
    await pro.uninstallToolPack('wh');
    expect(api.last.method).toBe('DELETE');
  });

  it('switch a tool with PATCH', async () => {
    api.reply('PATCH', '/api/v1/tool-packs/wh/tools/query', { enabled: false });
    await pro.setToolPackTool('wh', 'query', { enabled: false });
    expect(api.last.method).toBe('PATCH');
    expect(api.last.body).toEqual({ enabled: false });
  });

  it('preview and import an OpenAPI document', async () => {
    api.reply('POST', '/api/v1/tool-packs/openapi/preview', { title: 'Petstore', operations: [] });
    await pro.previewOpenApi({ url: 'https://example.com/openapi.yaml' });
    expect(api.last.body).toEqual({ url: 'https://example.com/openapi.yaml' });
    api.reply('POST', '/api/v1/tool-packs/openapi/import', { name: 'pets' }, 201);
    await pro.importOpenApi({ name: 'pets', spec: 'x', operations: ['getPet'], auth: { type: 'bearer' }, secrets: { token: 'vault://pets' } });
    expect(api.last.body).toEqual({ name: 'pets', spec: 'x', operations: ['getPet'], auth: { type: 'bearer' }, secrets: { token: 'vault://pets' } });
  });

  it('manage the egress allowlist', async () => {
    api.reply('GET', '/api/v1/tool-packs/egress', { hosts: [{ host: 'db.internal' }], platform: [] });
    expect((await pro.listEgressHosts()).hosts[0].host).toBe('db.internal');
    api.reply('POST', '/api/v1/tool-packs/egress', {}, 204);
    await pro.allowEgressHost('db.internal');
    expect(api.last.body).toEqual({ host: 'db.internal' });
    api.reply('DELETE', '/api/v1/tool-packs/egress/db.internal', {}, 204);
    await pro.removeEgressHost('db.internal');
    expect(api.last.method).toBe('DELETE');
  });
});
