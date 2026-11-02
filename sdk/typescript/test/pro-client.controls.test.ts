import { afterAll, beforeAll, beforeEach, describe, expect, it } from 'vitest';
import {
  AgentOvenAPIError, BudgetExceededError, ProClient, RateLimitedError, SIGNING_GATES, SIGNING_TOKEN_HEADER, budgetRecordId,
} from '../src/pro-client.js';
import { FakeControlPlane } from './fake-server.js';

const api = new FakeControlPlane();
let pro: ProClient;
const DIGEST = 'd'.repeat(64);
const TOKEN = SIGNING_TOKEN_HEADER.toLowerCase();

beforeAll(async () => {
  await api.start();
});
afterAll(async () => {
  await api.stop();
});
beforeEach(() => {
  api.requests = [];
  pro = new ProClient({ url: api.url, apiKey: 'ao_key', kitchen: 'acme' });
  api.reply('POST', '/api/v1/signatures/challenge', { subject: { digest: DIGEST, title: 't' }, reauth: 'password' });
  api.reply('POST', '/api/v1/signatures/token', { signing_token: 'tok.sig', expires_at: '2026-11-02T00:00:00Z', digest: DIGEST });
});

describe('budgets', () => {
  it('has the budget_raise gate, and record ids name the scope, who and the unit', () => {
    expect(SIGNING_GATES).toContain('budget_raise');
    expect(budgetRecordId()).toBe('kitchen::usd');
    expect(budgetRecordId({ unit: 'tokens' })).toBe('kitchen::tokens');
    expect(budgetRecordId({ provider: 'openai' })).toBe('provider:openai:usd');
    expect(budgetRecordId({ agent: 'triage', unit: 'tokens' })).toBe('agent:triage:tokens');
    expect(budgetRecordId({ apiKey: 'scopedkey:abc' })).toBe('api_key:scopedkey:abc:usd');
    expect(() => budgetRecordId({ agent: 'a', provider: 'p' })).toThrow(/not a mix/);
    expect(() => budgetRecordId({ unit: 'euros' as never })).toThrow(/unit/);
  });

  it('lists the budgets', async () => {
    api.reply('GET', '/api/v1/spend/budgets', { budgets: [{ scope: 'agent', agent: 'triage', unit: 'tokens', amount: 5000 }], count: 1 });
    expect((await pro.listBudgets())[0].unit).toBe('tokens');
  });

  it('sends scope, unit, amount, period and thresholds, unsigned', async () => {
    api.reply('PUT', '/api/v1/spend/budgets', { scope: 'agent' });
    await pro.setBudget(5000, { unit: 'tokens', agent: 'triage', period: 'daily', thresholds: [{ pct: 50, action: 'alert' }, { pct: 100, action: 'block' }] });
    expect(api.last.body).toEqual({
      agent: 'triage', unit: 'tokens', amount: 5000, period: 'daily',
      thresholds: [{ pct: 50, action: 'alert' }, { pct: 100, action: 'block' }],
    });
    expect(api.last.headers[TOKEN]).toBeUndefined();
  });

  it('defaults to dollars and leaves the period to the server', async () => {
    api.reply('PUT', '/api/v1/spend/budgets', {});
    await pro.setBudget(100);
    expect(api.last.body).toEqual({ unit: 'usd', amount: 100 });
  });

  it('signs a raise for exactly this change', async () => {
    api.reply('PUT', '/api/v1/spend/budgets', {});
    await pro.setBudget(200, { provider: 'openai', password: 'pw' });
    const [challenge, , put] = api.requests;
    expect(challenge.body.gate).toBe('budget_raise');
    expect(challenge.body.record_id).toBe('provider:openai:usd');
    expect(challenge.body.payload).toEqual({ provider: 'openai', unit: 'usd', amount: 200 });
    expect(put.headers[TOKEN]).toBe('tok.sig');
    expect(put.body.password).toBeUndefined();
  });

  it('sends a token you hold, and refuses both at once', async () => {
    api.reply('PUT', '/api/v1/spend/budgets', {});
    await pro.setBudget(200, { signingToken: 'held.tok' });
    expect(api.last.headers[TOKEN]).toBe('held.tok');
    expect(api.requests).toHaveLength(1);
    await expect(pro.setBudget(200, { password: 'pw', signingToken: 'x' })).rejects.toThrow(/not both/);
  });

  it('passes the server refusal of an unsigned raise through', async () => {
    api.reply('PUT', '/api/v1/spend/budgets', { error: 'signature_required' }, 403);
    const err = await pro.setBudget(900).catch((e) => e);
    expect(err).toBeInstanceOf(AgentOvenAPIError);
    expect(err.statusCode).toBe(403);
    expect(err.detail.error).toBe('signature_required');
  });

  it('checks what it can before sending', async () => {
    await expect(pro.setBudget(1, { unit: 'euros' as never })).rejects.toThrow(/unit/);
    await expect(pro.setBudget(1, { period: 'yearly' as never })).rejects.toThrow(/period/);
    await expect(pro.setBudget(1, { agent: 'a', provider: 'p' })).rejects.toThrow(/not a mix/);
    await expect(pro.setBudget(1, { thresholds: [{ pct: 50, action: 'explode' as never }] })).rejects.toThrow(/action/);
    expect(api.requests).toEqual([]);
  });

  it('signs a removal with the delete payload', async () => {
    api.reply('DELETE', '/api/v1/spend/budgets?agent=triage&unit=tokens', { deleted: true });
    await pro.removeBudget({ agent: 'triage', unit: 'tokens', password: 'pw' });
    const [challenge, , del] = api.requests;
    expect(challenge.body.record_id).toBe('agent:triage:tokens');
    expect(challenge.body.payload).toEqual({ delete: true });
    expect(del.headers[TOKEN]).toBe('tok.sig');
  });

  it('removes unsigned when no signature is given', async () => {
    api.reply('DELETE', '/api/v1/spend/budgets?unit=usd', { deleted: true });
    await pro.removeBudget();
    expect(api.last.headers[TOKEN]).toBeUndefined();
  });

  it('reads the ledger, the alerts and the usage', async () => {
    api.reply('GET', '/api/v1/spend/ledger?budget_id=b1&since=2026-11-01T00%3A00%3A00Z&limit=5', { entries: [{ id: 3 }], count: 1 });
    api.reply('GET', '/api/v1/spend/alerts?limit=10', { alerts: [{ threshold_pct: 80, notified: false }], count: 1 });
    api.reply('GET', '/api/v1/spend/usage?group=model&days=7&limit=3', { group: 'model', usage: [{ group: 'gpt', cache_hits: 2 }], count: 1 });
    expect((await pro.listBudgetLedger({ budgetId: 'b1', since: '2026-11-01T00:00:00Z', limit: 5 }))[0].id).toBe(3);
    expect((await pro.listBudgetAlerts(10))[0].notified).toBe(false);
    expect((await pro.budgetUsage('model', { days: 7, limit: 3 }))[0].cache_hits).toBe(2);
    await expect(pro.budgetUsage('password' as never)).rejects.toThrow(/group/);
  });
});

describe('limits and cache', () => {
  it('round trips limits', async () => {
    api.reply('GET', '/api/v1/limits', { limits: [{ scope: 'agent', ref: 'triage', requests_per_minute: 30, rejected: 4 }], count: 1 });
    api.reply('PUT', '/api/v1/limits', { scope: 'agent' });
    api.reply('DELETE', '/api/v1/limits?scope=agent&ref=triage', { deleted: true });
    api.reply('DELETE', '/api/v1/limits?scope=kitchen', { deleted: true });
    expect((await pro.listLimits())[0].rejected).toBe(4);
    await pro.setLimit('agent', 'triage', { requestsPerMinute: 30, tokensPerMinute: 20000 });
    expect(api.last.body).toEqual({ scope: 'agent', ref: 'triage', requests_per_minute: 30, tokens_per_minute: 20000, invocations_per_minute: 0 });
    await pro.removeLimit('agent', 'triage');
    await pro.removeLimit('kitchen');
    await expect(pro.setLimit('planet' as never, 'x', { requestsPerMinute: 1 })).rejects.toThrow(/scope/);
    await expect(pro.setLimit('agent', 'x', {})).rejects.toThrow(/at least one/);
  });

  it('manages the cache', async () => {
    api.reply('GET', '/api/v1/cache', { policies: [], stats: { hits: 0 } });
    api.reply('PUT', '/api/v1/cache/policy', { mode: 'always' });
    api.reply('DELETE', '/api/v1/cache/policy?agent=bot', { deleted: true });
    api.reply('POST', '/api/v1/cache/purge', { purged_at: '2026-11-03T00:00:00Z' });
    expect((await pro.getCache()).stats.hits).toBe(0);
    await pro.setCachePolicy('always', { agent: 'bot', ttlSeconds: 120, shareAcrossPrincipals: true });
    expect(api.last.body).toEqual({ agent: 'bot', mode: 'always', share_across_principals: true, ttl_seconds: 120 });
    await pro.removeCachePolicy('bot');
    expect((await pro.purgeCache()).purged_at).toBeTruthy();
    expect(api.last.body).toEqual({});
    await pro.purgeCache('bot');
    expect(api.last.body).toEqual({ agent: 'bot' });
    await expect(pro.setCachePolicy('sometimes' as never)).rejects.toThrow(/mode/);
  });
});

describe('refusals', () => {
  it('a rate limit is a typed error that says when to come back, and is not retried', async () => {
    api.reply('GET', '/api/v1/limits', { error: 'rate_limited', message: 'agent triage is over its limit', scope: 'agent', limit: 'requests_per_minute', retry_after: 2 }, 429, { 'Retry-After': '7' });
    const err = await pro.listLimits().catch((e) => e);
    expect(err).toBeInstanceOf(RateLimitedError);
    expect(err).toBeInstanceOf(AgentOvenAPIError);
    expect([err.statusCode, err.retryAfter, err.scope, err.limit]).toEqual([429, 7, 'agent', 'requests_per_minute']);
    expect(err.reason).toMatch(/over its limit/);
    expect(api.requests).toHaveLength(1);
  });

  it('the gateway shape is the same error', async () => {
    api.reply('GET', '/api/v1/limits', { error: { message: 'slow down', type: 'rate_limit_error', code: 'rate_limit_exceeded', scope: 'api_key', retry_after: 4 } }, 429);
    const err = await pro.listLimits().catch((e) => e);
    expect(err).toBeInstanceOf(RateLimitedError);
    expect([err.retryAfter, err.scope, err.reason]).toEqual([4, 'api_key', 'slow down']);
  });

  it('a 429 that is not ours stays a plain error', async () => {
    api.reply('GET', '/api/v1/limits', { error: 'queue full' }, 429);
    const err = await pro.listLimits().catch((e) => e);
    expect(err).toBeInstanceOf(AgentOvenAPIError);
    expect(err).not.toBeInstanceOf(RateLimitedError);
  });

  it('a budget refusal says whose budget, in what unit, and when it resets', async () => {
    api.reply('GET', '/api/v1/spend/budgets', {
      error: { message: 'used up', type: 'budget_exceeded', code: 'budget_exceeded', scope: 'agent', unit: 'tokens', period: 'monthly', resets_at: '2026-12-01T00:00:00Z' },
    }, 402);
    const err = await pro.listBudgets().catch((e) => e);
    expect(err).toBeInstanceOf(BudgetExceededError);
    expect([err.scope, err.unit, err.period, err.resetsAt]).toEqual(['agent', 'tokens', 'monthly', '2026-12-01T00:00:00Z']);
  });

  it('a run that stopped at a budget is the same error, and other 402s stay plain', async () => {
    api.reply('GET', '/api/v1/spend/budgets', { error: 'run_budget_exhausted', message: 'm', scope: 'kitchen', dimension: 'tokens' }, 402);
    const err = await pro.listBudgets().catch((e) => e);
    expect(err).toBeInstanceOf(BudgetExceededError);
    expect([err.scope, err.unit]).toEqual(['kitchen', 'tokens']);
    api.reply('GET', '/api/v1/spend/budgets', { error: 'payment needed' }, 402);
    const other = await pro.listBudgets().catch((e) => e);
    expect(other).not.toBeInstanceOf(BudgetExceededError);
  });
});
