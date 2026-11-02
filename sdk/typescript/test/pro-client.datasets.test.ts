import { afterAll, beforeAll, beforeEach, describe, expect, it } from 'vitest';
import { AgentOvenAPIError, ProClient } from '../src/pro-client.js';
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

describe('datasets', () => {
  it('are created, listed, read and updated', async () => {
    api.reply('POST', '/api/v1/datasets', { id: 'ds1', name: 'refund-evals' }, 201);
    await pro.createDataset({ name: 'refund-evals', description: 'cases', retention_mode: 'regulated', min_retention_days: 365, purpose: 'GxP evidence' });
    expect(api.last.body).toEqual({ name: 'refund-evals', description: 'cases', retention_mode: 'regulated', min_retention_days: 365, purpose: 'GxP evidence' });
    api.reply('GET', '/api/v1/datasets', [{ id: 'ds1' }]);
    expect(await pro.listDatasets()).toEqual([{ id: 'ds1' }]);
    api.reply('GET', '/api/v1/datasets/ds1', { id: 'ds1', retention: { state: 'active' } });
    expect((await pro.getDataset('ds1')).retention).toEqual({ state: 'active' });
    api.reply('PUT', '/api/v1/datasets/ds1', { id: 'ds1' });
    await pro.updateDataset('ds1', { description: 'new', retention_mode: 'expire', ttl_days: 30 });
    expect(api.last.body).toEqual({ description: 'new', retention_mode: 'expire', ttl_days: 30 });
  });

  it('refuse a retention mode the server does not have, before sending', async () => {
    await expect(pro.createDataset({ name: 'x', retention_mode: 'forever' as never })).rejects.toThrow(/expire, persist, regulated/);
    await expect(pro.updateDataset('ds1', { retention_mode: 'keep' as never })).rejects.toThrow();
    expect(api.requests).toHaveLength(0);
  });

  it('take cases: added, listed, retired, erased', async () => {
    api.reply('POST', '/api/v1/datasets/ds1/cases', { id: 'c1' }, 201);
    await pro.addDatasetCase('ds1', { input: { q: 'refund?' }, expected_output: { a: 'yes' }, metadata: { tag: 'x' }, provenance: { agent: 'refunds' } });
    expect(api.last.body).toEqual({ input: { q: 'refund?' }, expected_output: { a: 'yes' }, metadata: { tag: 'x' }, provenance: { agent: 'refunds' } });
    api.reply('GET', '/api/v1/datasets/ds1/cases?retired=true&limit=10&offset=20', []);
    await pro.listDatasetCases('ds1', { retired: true, limit: 10, offset: 20 });
    api.reply('GET', '/api/v1/datasets/ds1/cases', [{ id: 'c1' }]);
    expect(await pro.listDatasetCases('ds1')).toEqual([{ id: 'c1' }]);
    api.reply('DELETE', '/api/v1/datasets/ds1/cases/c1', {});
    await pro.retireDatasetCase('ds1', 'c1');
    expect(api.last.method).toBe('DELETE');
    api.reply('POST', '/api/v1/datasets/ds1/cases/c1/erase', { case: { id: 'c1', erased: true }, already_erased: false });
    expect((await pro.eraseDatasetCase('ds1', 'c1', 'data subject request')).case.erased).toBe(true);
    expect(api.last.body).toEqual({ reason: 'data subject request' });
  });

  it('keep the difference between no expected output and null when a case comes from a trace', async () => {
    api.reply('POST', '/api/v1/datasets/ds1/cases/from-trace', { id: 'c2' }, 201);
    await pro.addDatasetCaseFromTrace('ds1', { trace_id: 't1', span_id: 's1' });
    expect(api.last.body).toEqual({ trace_id: 't1', span_id: 's1' });
    await pro.addDatasetCaseFromTrace('ds1', { trace_id: 't1', span_id: 's1', expected_output: null, metadata: { from: 'replay' } });
    expect(api.last.body).toEqual({ trace_id: 't1', span_id: 's1', expected_output: null, metadata: { from: 'replay' } });
    await pro.addDatasetCaseFromTrace('ds1', { trace_id: 't1', span_id: 's1', expected_output: 'ok' });
    expect(api.last.body.expected_output).toBe('ok');
  });

  it('list, fetch and verify versions', async () => {
    api.reply('GET', '/api/v1/datasets/ds1/versions', [{ version: 1 }]);
    expect(await pro.listDatasetVersions('ds1')).toEqual([{ version: 1 }]);
    api.reply('GET', '/api/v1/datasets/ds1/versions/2', { version: 2 });
    expect((await pro.getDatasetVersion('ds1', 2)).version).toBe(2);
    api.reply('GET', '/api/v1/datasets/ds1/versions/2/verify', { ok: true });
    expect((await pro.verifyDatasetVersion('ds1', 2)).ok).toBe(true);
  });

  it('publish a regulated version signed on the dataset_publish gate, and an ordinary one unsigned', async () => {
    api.reply('POST', '/api/v1/datasets/ds1/versions', { version: { version: 3 }, signed: true }, 201);
    expect((await pro.publishDataset('ds1', { password: 'pw' })).signed).toBe(true);
    expect(api.requests[0].body).toEqual({ gate: 'dataset_publish', record_id: 'ds1', meaning: 'approved', payload: {} });
    expect(api.last.headers['x-signing-token']).toBe('tok.sig');

    api.requests = [];
    api.reply('POST', '/api/v1/datasets/ds2/versions', { version: { version: 1 }, signed: false }, 201);
    await pro.publishDataset('ds2');
    expect(api.requests).toHaveLength(1);
    expect(api.last.headers['x-signing-token']).toBeUndefined();

    api.reply('POST', '/api/v1/datasets/ds3/versions', { error: 'empty_version', message: 'no cases' }, 422);
    const err = await pro.publishDataset('ds3').catch((e) => e);
    expect(err).toBeInstanceOf(AgentOvenAPIError);
    expect(err.statusCode).toBe(422);
  });

  it('export returns the manifest, the cases and the untouched text', async () => {
    const manifest = { type: 'manifest', version: 2, dataset: 'refund-evals', manifest_digest: 'abc' };
    const c = { type: 'case', position: 1, content_hash: 'h1', input: { q: 1 } };
    const text = `${JSON.stringify(manifest)}\n${JSON.stringify(c)}\n`;
    api.replyRaw('GET', '/api/v1/datasets/ds1/export?version=2', text, { 'X-Manifest-Digest': 'abc' });
    const out = await pro.exportDataset('ds1', 2);
    expect(out.version).toBe(2);
    expect(out.manifest_digest).toBe('abc');
    expect(out.manifest.dataset).toBe('refund-evals');
    expect(out.cases).toEqual([c]);
    expect(out.text).toBe(text);
    api.replyRaw('GET', '/api/v1/datasets/ds1/export', text, { 'X-Manifest-Digest': 'abc' });
    expect((await pro.exportDataset('ds1')).version).toBe(2);
  });

  it('export that is refused rejects', async () => {
    api.reply('GET', '/api/v1/datasets/ds9/export', { error: 'no_versions', message: 'none' }, 409);
    const err = await pro.exportDataset('ds9').catch((e) => e);
    expect(err.statusCode).toBe(409);
  });

  it('lower a minimum signed with the payload the server signs', async () => {
    api.reply('POST', '/api/v1/datasets/ds1/retention/lower-minimum', { id: 'ds1' });
    await pro.lowerDatasetMinimum('ds1', 30, 'policy change', { password: 'pw' });
    expect(api.requests[0].body).toMatchObject({ gate: 'dataset_purge', record_id: 'ds1', payload: { action: 'lower_minimum', new_minimum_days: 30, reason: 'policy change' } });
    expect(api.last.body).toEqual({ min_retention_days: 30, reason: 'policy change' });
    expect(api.last.headers['x-signing-token']).toBe('tok.sig');
  });

  it('purge signed on the same gate with its own action', async () => {
    api.reply('POST', '/api/v1/datasets/ds1/purge', { tombstone: { id: 't' }, signed: true });
    expect((await pro.purgeDataset('ds1', 'retention over', { password: 'pw' })).signed).toBe(true);
    expect(api.requests[0].body.payload).toEqual({ action: 'purge', reason: 'retention over' });
    expect(api.last.body).toEqual({ reason: 'retention over' });
    expect(api.last.headers['x-signing-token']).toBe('tok.sig');
  });

  it('place a legal hold unsigned and lift it on the legal_hold_release gate', async () => {
    api.reply('PUT', '/api/v1/datasets/ds1/legal-hold', { id: 'ds1', legal_hold: true });
    await pro.placeDatasetLegalHold('ds1', 'litigation 42');
    expect(api.requests).toHaveLength(1);
    expect(api.last.body).toEqual({ held: true, reason: 'litigation 42' });
    expect(api.last.headers['x-signing-token']).toBeUndefined();

    await pro.liftDatasetLegalHold('ds1', 'case closed', { password: 'pw' });
    expect(api.requests[1].body).toEqual({ gate: 'legal_hold_release', record_id: 'dataset:ds1', meaning: 'approved', payload: { reason: 'case closed' } });
    expect(api.last.method).toBe('PUT');
    expect(api.last.body).toEqual({ held: false, reason: 'case closed' });
    expect(api.last.headers['x-signing-token']).toBe('tok.sig');
  });

  it('lift a hold with a token you hold, and surface the server refusing someone who is not legal', async () => {
    api.reply('PUT', '/api/v1/datasets/ds1/legal-hold', { id: 'ds1' });
    await pro.liftDatasetLegalHold('ds1', 'closed', { signingToken: 'held' });
    expect(api.requests).toHaveLength(1);
    expect(api.last.headers['x-signing-token']).toBe('held');
    api.reply('PUT', '/api/v1/datasets/ds1/legal-hold', { error: 'legal_only', message: 'only the legal team may lift a legal hold' }, 403);
    const err = await pro.liftDatasetLegalHold('ds1', 'x', { signingToken: 't' }).catch((e) => e);
    expect(err.statusCode).toBe(403);
    expect(err.detail.error).toBe('legal_only');
  });

  it('place the audit hold unsigned and lift it on the kitchen record', async () => {
    api.reply('POST', '/api/v1/audit/legal-hold', { hold: false });
    await pro.placeAuditLegalHold('investigation');
    expect(api.last.body).toEqual({ hold: true, reason: 'investigation' });
    expect(api.last.headers['x-signing-token']).toBeUndefined();
    await pro.liftAuditLegalHold('closed', { password: 'pw' });
    expect(api.requests[1].body).toMatchObject({ gate: 'legal_hold_release', record_id: 'audit:acme', payload: { reason: 'closed' } });
    expect(api.last.body).toEqual({ hold: false, reason: 'closed' });
    expect(api.last.headers['x-signing-token']).toBe('tok.sig');
  });

  it('list what expires, what is due for review, and the tombstones', async () => {
    api.reply('GET', '/api/v1/datasets/expiring', [{ id: 'a' }]);
    expect(await pro.listExpiringDatasets()).toEqual([{ id: 'a' }]);
    api.reply('GET', '/api/v1/datasets/expiring?days=7', []);
    await pro.listExpiringDatasets(7);
    expect(api.last.path).toBe('/api/v1/datasets/expiring?days=7');
    api.reply('GET', '/api/v1/datasets/reviews?days=14', [{ id: 'b' }]);
    expect(await pro.listDatasetReviews(14)).toEqual([{ id: 'b' }]);
    api.reply('GET', '/api/v1/datasets/tombstones', [{ dataset: 'old' }]);
    expect(await pro.listDatasetTombstones()).toEqual([{ dataset: 'old' }]);
  });
});
