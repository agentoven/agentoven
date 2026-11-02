import { afterAll, beforeAll, beforeEach, describe, expect, it } from 'vitest';
import { AgentOvenAPIError, KITCHEN_ROLES, OidcSigningRequired, PERSON_ROLES, ProClient, SIGNING_GATES, SIGNING_TOKEN_HEADER } from '../src/pro-client.js';
import { FakeControlPlane } from './fake-server.js';

const api = new FakeControlPlane();
let pro: ProClient;
const DIGEST = 'd'.repeat(64);

beforeAll(async () => {
  await api.start();
});
afterAll(async () => {
  await api.stop();
});
beforeEach(() => {
  api.requests = [];
  pro = new ProClient({ url: api.url, apiKey: 'ao_key', kitchen: 'acme' });
  api.reply('POST', '/api/v1/signatures/challenge', { subject: { digest: DIGEST, title: 't' }, reauth: 'password', meaning: 'approved' });
  api.reply('POST', '/api/v1/signatures/token', { signing_token: 'tok.sig', expires_at: '2026-11-02T00:00:00Z', digest: DIGEST });
});

describe('sign', () => {
  it('names the header the control plane reads and the new gates', () => {
    expect(SIGNING_TOKEN_HEADER).toBe('X-Signing-Token');
    expect(SIGNING_GATES).toContain('legal_hold_release');
    expect(SIGNING_GATES).toContain('approval_skip');
  });

  it('runs the challenge then the token and returns the token', async () => {
    const token = await pro.sign('legal_hold_release', 'dataset:ds1', 'approved', { reason: 'case closed' }, 'pw');
    expect(token).toBe('tok.sig');
    const [challenge, issued] = api.requests;
    expect(challenge.path).toBe('/api/v1/signatures/challenge');
    expect(challenge.body).toEqual({ gate: 'legal_hold_release', record_id: 'dataset:ds1', meaning: 'approved', payload: { reason: 'case closed' } });
    expect(issued.body).toEqual({ ...challenge.body, digest: DIGEST, password: 'pw' });
  });

  it('sends an empty payload when there is none, and the older signDecision is the same call', async () => {
    await pro.sign('recipe_gate', 'r1:s', 'rejected', undefined, 'pw');
    expect(api.requests[0].body.payload).toEqual({});
    expect(await pro.signDecision('recipe_gate', 'r1:s', 'approved', 'pw', { a: 1 })).toBe('tok.sig');
    expect(api.requests[2].body.payload).toEqual({ a: 1 });
  });

  it('refuses a meaning that is not one', async () => {
    await expect(pro.sign('recipe_gate', 'r1:s', 'maybe' as never, undefined, 'pw')).rejects.toThrow(/approved/);
  });

  it('tells an OIDC account so, and the password goes nowhere', async () => {
    api.reply('POST', '/api/v1/signatures/challenge', { subject: { digest: DIGEST }, reauth: 'oidc' });
    const err = await pro.sign('recipe_gate', 'r1:s', 'approved', undefined, 'pw').catch((e) => e);
    expect(err).toBeInstanceOf(OidcSigningRequired);
    expect(err.reason).toMatch(/identity provider/);
    expect(api.requests.map((r) => r.path)).toEqual(['/api/v1/signatures/challenge']);
  });

  it('treats use_oidc from the token endpoint the same way, and leaves other refusals alone', async () => {
    api.reply('POST', '/api/v1/signatures/token', { error: 'use_oidc', message: 'x' }, 400);
    await expect(pro.sign('recipe_gate', 'r1:s', 'approved', undefined, 'pw')).rejects.toBeInstanceOf(OidcSigningRequired);
    api.reply('POST', '/api/v1/signatures/challenge', { error: 'signature_required', message: 'no' }, 403);
    const err = await pro.sign('recipe_gate', 'r1:s', 'approved', undefined, 'pw').catch((e) => e);
    expect(err).toBeInstanceOf(AgentOvenAPIError);
    expect(err).not.toBeInstanceOf(OidcSigningRequired);
    expect(err.statusCode).toBe(403);
  });
});

describe('passing the token on a decision', () => {
  it('a token you hold goes in the header and nothing is signed', async () => {
    api.reply('POST', '/api/v1/approvals/r1/s', { approved: true });
    await pro.approveGate('r1', 's', undefined, { signingToken: 'held.tok' });
    expect(api.requests).toHaveLength(1);
    expect(api.last.headers['x-signing-token']).toBe('held.tok');
    expect(api.last.headers['authorization']).toBe('Bearer ao_key');
  });

  it('a password and a token together are a mistake', async () => {
    await expect(pro.approveGate('r1', 's', undefined, { password: 'pw', signingToken: 't' })).rejects.toThrow(/not both/);
    expect(api.requests).toHaveLength(0);
  });

  it('signedRequest sends any call with the header', async () => {
    api.reply('POST', '/api/v1/promotions', { id: 'p1' });
    await pro.signedRequest('POST', '/api/v1/promotions', { agent_name: 'a' }, 'tok.sig');
    expect(api.last.headers['x-signing-token']).toBe('tok.sig');
    expect(api.last.body).toEqual({ agent_name: 'a' });
  });
});

describe('approval_skip', () => {
  it('signs <recipe>@<environment> with the input and sends the token with the run', async () => {
    api.reply('POST', '/api/v1/recipes/refunds/bake', { run_id: 'r9', status: 'running' });
    const out = (await pro.runRecipe('refunds', { order: 7 }, { environment: 'prod', password: 'pw' })) as { run_id: string };
    expect(out.run_id).toBe('r9');
    expect(api.requests[0].body).toEqual({ gate: 'approval_skip', record_id: 'refunds@prod', meaning: 'approved', payload: { input: { order: 7 } } });
    expect(api.last.headers['x-signing-token']).toBe('tok.sig');
    expect(api.last.body).toEqual({ input: { order: 7 }, environment: 'prod' });
  });

  it('is not signed without a password, and needs the environment named when it is', async () => {
    api.reply('POST', '/api/v1/recipes/refunds/bake', { run_id: 'r9' });
    await pro.runRecipe('refunds', { order: 7 }, { environment: 'dev' });
    expect(api.requests).toHaveLength(1);
    expect(api.last.headers['x-signing-token']).toBeUndefined();
    await expect(pro.runRecipe('refunds', {}, { password: 'pw' })).rejects.toThrow(/environment/);
  });
});

describe('signatures and approver policy', () => {
  it('lists, fetches and verifies', async () => {
    api.reply('GET', '/api/v1/signatures?gate=dataset_purge&record_id=ds1', [{ id: 's1' }]);
    expect((await pro.listSignatures('dataset_purge', 'ds1'))[0].id).toBe('s1');
    api.reply('GET', '/api/v1/signatures/s%2F1', { id: 's/1', meaning: 'approved' });
    expect((await pro.getSignature('s/1')).meaning).toBe('approved');
    api.reply('GET', '/api/v1/signatures/verify', { ok: true, count: 3 });
    expect((await pro.verifySignatures()).ok).toBe(true);
  });

  it('reads and replaces the approver policy', async () => {
    api.reply('GET', '/api/v1/approver-policy', { policy: {}, gates: ['tool_approval'] });
    expect((await pro.getApproverPolicy()).gates).toEqual(['tool_approval']);
    api.reply('PUT', '/api/v1/approver-policy', { policy: {} });
    await pro.setApproverPolicy({ 'environment_promotion:prod': { roles: ['head_chef', 'legal'] }, legal_hold_release: { roles: ['legal'], users: ['ana@acme.io'] } });
    expect(api.last.body).toEqual({
      policy: {
        'environment_promotion:prod': { roles: ['head_chef', 'legal'], users: [] },
        legal_hold_release: { roles: ['legal'], users: ['ana@acme.io'] },
      },
    });
  });

  it('refuses the retired admin and unknown roles before sending', async () => {
    await expect(pro.setApproverPolicy({ tool_approval: { roles: ['admin'] } })).rejects.toThrow(/retired/);
    await expect(pro.setApproverPolicy({ tool_approval: { roles: ['sous_chef'] } })).rejects.toThrow(/unknown role/);
    expect(api.requests).toHaveLength(0);
  });
});

describe('roles', () => {
  it('are the real ones', () => {
    expect([...KITCHEN_ROLES]).toEqual(['head_chef', 'chef', 'baker', 'taster', 'auditor', 'legal', 'finance', 'viewer']);
    expect([...PERSON_ROLES]).toEqual(['restaurateur', ...KITCHEN_ROLES]);
    expect(PERSON_ROLES as readonly string[]).not.toContain('admin');
  });

  it('are checked where they are given', async () => {
    await expect(pro.createServiceAccount('ci', 'admin' as never)).rejects.toThrow(/retired/);
    await expect(pro.createServiceAccount('ci', 'restaurateur' as never)).rejects.toThrow(/head_chef/);
    await expect(pro.createUser('a@b.c', 'admin' as never)).rejects.toThrow(/retired/);
    await expect(pro.addKitchenMember('k1', 'u1', 'restaurateur' as never)).rejects.toThrow(/platform-wide/);
    expect(api.requests).toHaveLength(0);
    api.reply('POST', '/api/v1/users', { id: 'u1' });
    await pro.createUser('ana@acme.io', 'restaurateur', 'Ana');
    expect(api.last.body.role).toBe('restaurateur');
    await pro.createUser('bo@acme.io', 'legal');
    expect(api.last.body.role).toBe('legal');
  });
});
