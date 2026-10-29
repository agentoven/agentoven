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
  api.reply('POST', '/api/v1/signatures/challenge', { subject: { digest: 'd'.repeat(64), title: 'Approve step' }, reauth: 'password' });
  api.reply('POST', '/api/v1/signatures/token', { signing_token: 'tok.sig', expires_at: '2026-10-29T00:00:00Z' });
  api.reply('POST', '/api/v1/approvals/r1/sign-off', { approved: true, signed_by: 'ana' });
});

describe('signed decisions', () => {
  it('approveGate with a password asks for the challenge, proves who you are, and sends the token', async () => {
    const out = (await pro.approveGate('r1', 'sign-off', 'ok', { password: 'pw' })) as { signed_by: string };
    expect(out.signed_by).toBe('ana');
    const [challenge, token, decision] = api.requests.slice(-3);
    expect(challenge.body).toMatchObject({ gate: 'recipe_gate', record_id: 'r1:sign-off', meaning: 'approved' });
    expect(token.body).toMatchObject({ digest: 'd'.repeat(64), password: 'pw' });
    expect(challenge.body.password).toBeUndefined();
    expect(decision.body.password).toBeUndefined();
    expect(decision.headers['x-signing-token']).toBe('tok.sig');
    expect(decision.headers['authorization']).toBe('Bearer ao_key'); // the caller's own credentials still go along
  });

  it('rejectGate asks for the other meaning, and releaseWait is its own gate', async () => {
    await pro.rejectGate('r1', 'sign-off', 'no', { password: 'pw' });
    expect(api.requests.slice(-3)[0].body.meaning).toBe('rejected');
    api.reply('POST', '/api/v1/recipes/flow/runs/r1/steps/pause/release', { released: true });
    await pro.releaseWait('flow', 'r1', 'pause', { password: 'pw' });
    expect(api.requests.slice(-3)[0].body).toMatchObject({ gate: 'wait_release', record_id: 'r1:pause' });
    expect(api.last.headers['x-signing-token']).toBe('tok.sig');
  });

  it('without a password the decision is unsigned', async () => {
    await pro.approveGate('r1', 'sign-off');
    expect(api.requests).toHaveLength(1);
    expect(api.last.headers['x-signing-token']).toBeUndefined();
  });

  it('lists signatures and checks the chain', async () => {
    api.reply('GET', '/api/v1/signatures?gate=recipe_gate&record_id=r1%3Asign-off', [{ printed_name: 'ana', meaning: 'approved' }]);
    api.reply('GET', '/api/v1/signatures/verify', { ok: true, count: 1 });
    expect((await pro.listSignatures('recipe_gate', 'r1:sign-off'))[0].printed_name).toBe('ana');
    expect((await pro.verifySignatures()).ok).toBe(true);
  });
});
