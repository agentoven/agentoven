import { afterAll, beforeAll, beforeEach, describe, expect, it } from 'vitest';
import { ProClient } from '../src/pro-client.js';
import { signWebhook } from '../src/triggers.js';
import { FakeControlPlane } from './fake-server.js';

const SIG = 'bf726e7ee478ebfa6f2d2508dbdb022a8c741e1a38cf36630f5c6004ab9dd8b3';
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

describe('triggers', () => {
  it('are created with the body the server expects and return the secret once', async () => {
    api.reply('POST', '/api/v1/triggers', { trigger: { id: 't1', url_path: '/hooks/acme/inv' }, secret: 'whsec_x' }, 201);
    const out = await pro.createTrigger({ name: 'inv', service_account_id: 'sa1', target: 'invoice-flow', environment: 'staging', input_mapping: { id: '$.invoice.id' }, required_paths: ['$.invoice.id'], rate_limit_per_min: 30 });
    expect(out.secret).toBe('whsec_x');
    expect(api.last.body).toEqual({
      enabled: true, name: 'inv', service_account_id: 'sa1', target: 'invoice-flow', environment: 'staging', input_mapping: { id: '$.invoice.id' },
      required_paths: ['$.invoice.id'], rate_limit_per_min: 30, kind: 'webhook', target_kind: 'recipe',
    });
    expect(api.last.headers['x-kitchen']).toBe('acme');
  });

  it('take an agent as the target and a mailbox as the source', async () => {
    api.reply('POST', '/api/v1/triggers', { trigger: { id: 't2' } }, 201);
    await pro.createTrigger({ name: 'mail', service_account_id: 'sa1', target: 'reader', kind: 'email', target_kind: 'agent', config: { host: 'imap.example.com' } });
    expect(api.last.body).toMatchObject({ kind: 'email', target_kind: 'agent', config: { host: 'imap.example.com' } });
  });

  it('refuse, before sending, what the server would refuse', async () => {
    await expect(pro.createTrigger({ name: 'x', service_account_id: 's', target: 't', kind: 'pigeon' as never })).rejects.toThrow(/webhook, email, file/);
    await expect(pro.createTrigger({ name: 'x', service_account_id: 's', target: 't', target_kind: 'tool' as never })).rejects.toThrow(/recipe or agent/);
    await expect(pro.listTriggerDeliveries({ status: 'bogus' as never })).rejects.toThrow(/accepted, rejected, duplicate, failed/);
    expect(api.requests).toHaveLength(0);
  });

  it('are listed, read, changed, switched, rotated and deleted', async () => {
    api.reply('GET', '/api/v1/triggers', { triggers: [{ id: 't1' }], count: 1 });
    expect(await pro.listTriggers()).toEqual([{ id: 't1' }]);
    api.reply('GET', '/api/v1/triggers/t1', { id: 't1' });
    expect(await pro.getTrigger('t1')).toEqual({ id: 't1' });
    api.reply('PUT', '/api/v1/triggers/t1', { id: 't1' });
    await pro.setTriggerEnabled('t1', false);
    expect(api.last.body).toEqual({ enabled: false });
    await pro.updateTrigger('t1', { target: 'other' });
    expect(api.last.body).toEqual({ target: 'other' });
    api.reply('POST', '/api/v1/triggers/t1/rotate-secret', { secret: 'whsec_new' });
    expect((await pro.rotateTriggerSecret('t1')).secret).toBe('whsec_new');
    api.reply('DELETE', '/api/v1/triggers/t1', {});
    await pro.deleteTrigger('t1');
    expect(api.last.method).toBe('DELETE');
  });

  it('cannot have an id that changes the route', async () => {
    api.reply('GET', '/api/v1/triggers/..%2F..%2Fusers', { id: 'x' });
    await pro.getTrigger('../../users');
    expect(api.last.path).toBe('/api/v1/triggers/..%2F..%2Fusers');
  });

  it('are fired as a dry run unless said otherwise', async () => {
    api.reply('POST', '/api/v1/triggers/t1/fire', { status: 'dry_run', dry_run: true });
    await pro.fireTrigger('t1', { a: 1 });
    expect(api.last.body).toEqual({ payload: { a: 1 }, dry_run: true });
    await pro.fireTrigger('t1', { a: 1 }, { dryRun: false });
    expect(api.last.body.dry_run).toBe(false);
    api.reply('POST', '/api/v1/triggers/t1/poll', { handled: 2 });
    expect(await pro.pollTrigger('t1')).toEqual({ handled: 2 });
  });

  it('have deliveries filtered in the query', async () => {
    api.reply('GET', '/api/v1/triggers/deliveries?trigger_id=t1&status=failed&limit=10', { deliveries: [{ id: 'd1' }] });
    expect(await pro.listTriggerDeliveries({ trigger_id: 't1', status: 'failed', limit: 10 })).toEqual([{ id: 'd1' }]);
    api.reply('GET', '/api/v1/triggers/deliveries', { deliveries: [] });
    expect(await pro.listTriggerDeliveries()).toEqual([]);
  });

  it('are signed the way the server verifies', async () => {
    const body = '{"hello":"world"}';
    const h = await signWebhook('whsec_abc', body, { timestamp: 1793448000, deliveryId: 'd-1' });
    // HMAC-SHA256("whsec_abc", "1793448000." + body), computed independently with openssl
    expect(h['X-AgentOven-Signature']).toBe('sha256=' + SIG);
    expect(h['X-AgentOven-Timestamp']).toBe('1793448000');
    expect(h['X-AgentOven-Delivery']).toBe('d-1');
    expect((await signWebhook('whsec_abc', body))['X-AgentOven-Delivery']).not.toBe((await signWebhook('whsec_abc', body))['X-AgentOven-Delivery']);
  });
});
