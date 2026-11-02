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

describe('guardrail rules with actions', () => {
  it('creates a rule with its action and stage', async () => {
    api.reply('POST', '/api/v1/guardrails', { id: 'g1', action: 'redact' }, 201);
    const rule = { name: 'pii', kind: 'pii_detection', stage: 'all', action: 'redact', config: { patterns: ['pii'] } } as const;
    expect((await pro.createGuardrail(rule)).action).toBe('redact');
    expect(api.last.body).toEqual(rule);
    expect(api.last.headers['x-kitchen']).toBe('acme');
  });

  it('updates and deletes by id', async () => {
    api.reply('PUT', '/api/v1/guardrails/g1', { id: 'g1', action: 'warn' });
    await pro.updateGuardrail('g1', { action: 'warn' });
    expect(api.last.body).toEqual({ action: 'warn' });
    api.reply('DELETE', '/api/v1/guardrails/g1', {}, 204);
    await pro.deleteGuardrail('g1');
    expect(api.last.method).toBe('DELETE');
  });

  it('flips a rule with PATCH, or sets it when told', async () => {
    api.reply('PATCH', '/api/v1/guardrails/g1/toggle', { enabled: false });
    await pro.toggleGuardrail('g1');
    expect(api.last.method).toBe('PATCH');
    api.reply('PUT', '/api/v1/guardrails/g1', { enabled: true });
    await pro.toggleGuardrail('g1', true);
    expect(api.last.method).toBe('PUT');
    expect(api.last.body).toEqual({ enabled: true });
  });

  it('finds one rule in the list', async () => {
    api.reply('GET', '/api/v1/guardrails', [{ id: 'g1', name: 'a' }, { id: 'g2', name: 'b' }]);
    expect((await pro.getGuardrail('g2')).name).toBe('b');
    await expect(pro.getGuardrail('nope')).rejects.toThrow(/no guardrail/);
  });

  it('lists the kinds, actions and detectors', async () => {
    api.reply('GET', '/api/v1/guardrails/kinds', { kinds: [], stages: ['input'], actions: ['block', 'redact'], detectors: ['iban'] });
    expect((await pro.guardrailKinds()).detectors).toEqual(['iban']);
  });

  it('tests a text and sends only what was given', async () => {
    api.reply('POST', '/api/v1/guardrails/test', { outcome: 'redacted', text: 'pay [IBAN]', redacted: 1, warned: 0, passed: true, results: [] });
    const out = await pro.testGuardrails('pay DE89370400440532013000', { stage: 'output' });
    expect(out.text).toBe('pay [IBAN]');
    expect(api.last.body).toEqual({ text: 'pay DE89370400440532013000', stage: 'output' });
    await pro.testGuardrails('x', { rules: [{ kind: 'pii_detection', action: 'mask' }], includeCustom: true });
    expect(api.last.body).toEqual({ text: 'x', stage: 'input', rules: [{ kind: 'pii_detection', action: 'mask' }], include_custom: true });
  });

  it('removes an exception by its own id', async () => {
    api.reply('POST', '/api/v1/guardrails/g1/exceptions', { id: 'e1' }, 201);
    await pro.addGuardrailException('g1', 'support-bot', 'migration', '2026-12-01T00:00:00Z');
    expect(api.last.body).toEqual({ agent_name: 'support-bot', reason: 'migration', expires_at: '2026-12-01T00:00:00Z' });
    api.reply('DELETE', '/api/v1/guardrails/g1/exceptions/e1', {}, 204);
    await pro.removeGuardrailException('g1', 'e1');
    expect(api.last.path).toBe('/api/v1/guardrails/g1/exceptions/e1');
  });
});
