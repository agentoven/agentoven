import { afterAll, beforeAll, beforeEach, describe, expect, it } from 'vitest';
import { ProClient, SIGNING_GATES } from '../src/pro-client.js';
import { FakeControlPlane } from './fake-server.js';

const api = new FakeControlPlane();
let pro: ProClient;
const DIGEST = 'f'.repeat(64);

beforeAll(async () => {
  await api.start();
});
afterAll(async () => {
  await api.stop();
});
beforeEach(() => {
  api.requests = [];
  pro = new ProClient({ url: api.url, apiKey: 'ao_key', kitchen: 'acme' });
  api.reply('POST', '/api/v1/signatures/challenge', { subject: { digest: DIGEST }, reauth: 'password' });
  api.reply('POST', '/api/v1/signatures/token', { signing_token: 'tok.sig' });
});

describe('secrets', () => {
  it('are created with a value or linked with a ref, and listed without values', async () => {
    api.reply('POST', '/api/v1/secrets', { name: 'firecrawl', provider: 'dbvault', current_version: '1' }, 201);
    await pro.createSecret({ name: 'firecrawl', value: 'fc-1', allowed_hosts: ['api.firecrawl.dev'], require_binding: true });
    expect(api.last.body).toEqual({ name: 'firecrawl', value: 'fc-1', allowed_hosts: ['api.firecrawl.dev'], require_binding: true });
    api.reply('POST', '/api/v1/secrets', { name: 'stripe', provider: 'mounted', linked: true }, 201);
    await pro.createSecret({ name: 'stripe', provider: 'mounted', ref: 'team/stripe' });
    expect(api.last.body).toEqual({ name: 'stripe', provider: 'mounted', ref: 'team/stripe' });
    api.reply('GET', '/api/v1/secrets', [{ name: 'firecrawl' }]);
    expect(await pro.listSecrets()).toEqual([{ name: 'firecrawl' }]);
    api.reply('GET', '/api/v1/secrets/firecrawl', { name: 'firecrawl' });
    expect((await pro.getSecret('firecrawl')).name).toBe('firecrawl');
    api.reply('PUT', '/api/v1/secrets/firecrawl', { name: 'firecrawl' });
    await pro.updateSecret('firecrawl', { clear_expiry: true, allowed_hosts: [] });
    expect(api.last.body).toEqual({ clear_expiry: true, allowed_hosts: [] });
  });

  it('are rotated with an optional probe, and refreshed when their owner rotates them', async () => {
    api.reply('PUT', '/api/v1/secrets/api/value', { version: '2', probed: true });
    await pro.rotateSecret('api', 'new', { probe: { url: 'https://api.example/ping', expect_status: 200 }, grace_seconds: 30 });
    expect(api.last.body).toEqual({ value: 'new', probe: { url: 'https://api.example/ping', expect_status: 200 }, grace_seconds: 30 });
    api.reply('POST', '/api/v1/secrets/api/refresh', { changed: true });
    expect((await pro.refreshSecret('api')).changed).toBe(true);
    api.reply('GET', '/api/v1/secrets/api/versions', [{ Version: '2', State: 'current' }]);
    expect(await pro.secretVersions('api')).toHaveLength(1);
  });

  it('take bindings that a person approves with a signature', async () => {
    api.reply('POST', '/api/v1/secrets/gh/bindings', { id: 'b1', status: 'pending' }, 201);
    await pro.proposeSecretBinding('gh', { skill: 'pr-review', server: 'api.github.com' });
    expect(api.last.body).toEqual({ skill: 'pr-review', server: 'api.github.com' });
    api.reply('POST', '/api/v1/secret-bindings/b1/decision', { id: 'b1', status: 'approved' });
    await pro.decideSecretBinding('b1', 'approved', { password: 'pw' });
    const challenge = api.requests.find((r) => r.path === '/api/v1/signatures/challenge')!;
    expect(challenge.body).toMatchObject({ gate: 'secret_binding', record_id: 'b1', meaning: 'approved' });
    expect(api.last.headers['x-signing-token']).toBe('tok.sig');
    await pro.decideSecretBinding('b1', 'rejected', { signingToken: 'held' });
    expect(api.last.body).toEqual({ meaning: 'rejected' });
    expect(api.last.headers['x-signing-token']).toBe('held');
    api.reply('DELETE', '/api/v1/secret-bindings/b2', {});
    await pro.withdrawSecretBinding('b2');
  });

  it('are migrated on a signed decision bound to the from:to record; a dry run needs none', async () => {
    api.reply('POST', '/api/v1/secrets/migrate', { moved: 1, secrets: [], conflicts: [], digest: 'dg' });
    await pro.planSecretMigration('dbvault', 'vault-a');
    expect(api.last.body).toEqual({ from: 'dbvault', to: 'vault-a', dry_run: true });
    expect(api.requests.some((r) => r.path === '/api/v1/signatures/challenge')).toBe(false);
    await pro.migrateSecrets('dbvault', 'vault-a', { password: 'pw' });
    const challenge = api.requests.find((r) => r.path === '/api/v1/signatures/challenge')!;
    expect(challenge.body).toMatchObject({ gate: 'secret_migrate', record_id: 'dbvault:vault-a', meaning: 'approved' });
    expect(api.last.body).toEqual({ from: 'dbvault', to: 'vault-a' });
    expect(api.last.headers['x-signing-token']).toBe('tok.sig');
  });

  it('manage providers', async () => {
    api.reply('GET', '/api/v1/secret-providers', [{ name: 'dbvault', writable: true }]);
    expect((await pro.listSecretProviders())[0].name).toBe('dbvault');
    api.reply('POST', '/api/v1/secret-providers', { name: 'vault-a' }, 201);
    await pro.createSecretProvider({ name: 'vault-a', kind: 'hashicorp', config: { base_path: 'team-a' } });
    expect(api.last.body).toEqual({ name: 'vault-a', kind: 'hashicorp', config: { base_path: 'team-a' } });
    api.reply('PUT', '/api/v1/secret-providers/vault-a/default?platform=true', {});
    await pro.setDefaultSecretProvider('vault-a', { platform: true });
    api.reply('DELETE', '/api/v1/secret-providers/vault-a', {});
    await pro.deleteSecretProvider('vault-a');
  });

  it('know the two new signing gates', () => {
    expect(SIGNING_GATES).toContain('secret_binding');
    expect(SIGNING_GATES).toContain('secret_migrate');
  });
});
