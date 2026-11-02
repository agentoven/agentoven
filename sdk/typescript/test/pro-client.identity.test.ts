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

describe('kitchen bindings', () => {
  it('are listed, created and deleted', async () => {
    api.reply('GET', '/api/v1/kitchens/k1/bindings', [{ id: 'b1', claim_value: 'g-1', role: 'chef' }]);
    expect((await pro.listKitchenBindings('k1'))[0].role).toBe('chef');
    api.reply('POST', '/api/v1/kitchens/k1/bindings', { id: 'b2' }, 201);
    await pro.createKitchenBinding('k1', 'aad-group-guid', 'taster', 'QA reviewers');
    expect(api.last.body).toEqual({ claim_value: 'aad-group-guid', role: 'taster', label: 'QA reviewers' });
    api.reply('DELETE', '/api/v1/kitchens/k1/bindings/b2', {});
    await pro.deleteKitchenBinding('k1', 'b2');
    expect(api.last.method).toBe('DELETE');
  });

  it('take a kitchen role only, checked before sending', async () => {
    await expect(pro.createKitchenBinding('k1', 'g', 'admin' as never)).rejects.toThrow(/retired/);
    await expect(pro.createKitchenBinding('k1', 'g', 'restaurateur' as never)).rejects.toThrow(/platform-wide/);
    await expect(pro.createKitchenBinding('k1', 'g', 'sous_chef' as never)).rejects.toThrow(/unknown role/);
    await expect(pro.createKitchenBinding('k1', '  ', 'chef')).rejects.toThrow(/claimValue/);
    expect(api.requests).toHaveLength(0);
  });

  it('surface the code the server refuses with', async () => {
    api.reply('POST', '/api/v1/kitchens/k1/bindings', { error: 'binding_head_chef_requires_restaurateur', message: 'm' }, 403);
    const err = await pro.createKitchenBinding('k1', 'g', 'head_chef').catch((e) => e);
    expect(err.detail.error).toBe('binding_head_chef_requires_restaurateur');
  });
});

describe('credential owners', () => {
  it('are set on service accounts and scoped keys', async () => {
    api.reply('POST', '/api/v1/service-accounts/sa1/owner', { id: 'sa1', owner_email: 'ana@acme.io' });
    expect((await pro.setServiceAccountOwner('sa1', 'ana@acme.io')).owner_email).toBe('ana@acme.io');
    expect(api.last.body).toEqual({ owner_email: 'ana@acme.io' });
    api.reply('POST', '/api/v1/keys/k9/owner', { id: 'k9', owner_email: 'bo@acme.io' });
    await pro.setScopedKeyOwner('k9', 'bo@acme.io');
    expect(api.last.path).toBe('/api/v1/keys/k9/owner');
    expect(api.last.body).toEqual({ owner_email: 'bo@acme.io' });
  });

  it('are named when a credential is created', async () => {
    api.reply('POST', '/api/v1/service-accounts', { id: 'sa1', token: 'ao_sa_x' }, 201);
    await pro.createServiceAccount('ci', 'baker', { ownerEmail: 'ana@acme.io' });
    expect(api.last.body).toEqual({ name: 'ci', role: 'baker', owner_email: 'ana@acme.io' });
    await pro.createServiceAccount('ci2', 'baker');
    expect(api.last.body).toEqual({ name: 'ci2', role: 'baker' });
    api.reply('POST', '/api/v1/keys', { id: 'k1' }, 201);
    await pro.createScopedKey({ label: 'demo', agent_names: ['*'], owner_email: 'bo@acme.io' });
    expect(api.last.body.owner_email).toBe('bo@acme.io');
  });
});

describe('my kitchens', () => {
  it('says which kitchens you can enter and as what', async () => {
    api.reply('GET', '/api/v1/me/kitchens', { enforced: true, kitchens: [{ id: 'k1', name: 'acme', role: 'head_chef' }] });
    const out = await pro.myKitchens();
    expect(out.enforced).toBe(true);
    expect(out.kitchens[0].role).toBe('head_chef');
  });
});

describe('agent versions', () => {
  it('are listed and fetched', async () => {
    api.reply('GET', '/api/v1/agents/refund-bot/versions', [{ version: '0.1.0' }, { version: '0.2.0' }]);
    expect((await pro.listAgentVersions('refund-bot')).map((v) => v.version)).toEqual(['0.1.0', '0.2.0']);
    api.reply('GET', '/api/v1/agents/refund-bot/versions/0.1.0', { name: 'refund-bot', version: '0.1.0' });
    expect((await pro.getAgentVersion('refund-bot', '0.1.0')).version).toBe('0.1.0');
    api.reply('GET', '/api/v1/agents/refund-bot/versions/9.9.9', { error: 'not found' }, 404);
    const err = await pro.getAgentVersion('refund-bot', '9.9.9').catch((e) => e);
    expect(err.statusCode).toBe(404);
  });
});
