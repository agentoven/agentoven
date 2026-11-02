import { afterAll, beforeAll, beforeEach, describe, expect, it } from 'vitest';
import { AgentOvenAPIError, ProClient } from '../src/pro-client.js';
import { FakeControlPlane } from './fake-server.js';

const api = new FakeControlPlane();
let pro: ProClient;
const DIGEST = 'e'.repeat(64);

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

describe('replayCall', () => {
  it('re-runs a captured call as variants', async () => {
    api.reply('POST', '/api/v1/traces/t1/spans/s1/replay', { notice: 'This is a replay', variants: [{ index: 0, status: 'completed' }] });
    const variants = [{ label: 'cooler', edits: { temperature: 0.1 } }, { edits: { model: 'claude-haiku', messages: [{ index: 0, content: 'hi' }] } }];
    const out = await pro.replayCall('t1', 's1', { variants, reason: 'why did it refuse' });
    expect(out.variants[0].status).toBe('completed');
    expect(api.last.body).toEqual({ variants, reason: 'why did it refuse' });
    expect(api.last.headers['x-kitchen']).toBe('acme');
  });

  it('with no variants re-runs it as captured, and escapes ids in the path', async () => {
    api.reply('POST', '/api/v1/traces/t%2F1/spans/s%201/replay', { variants: [] });
    await pro.replayCall('t/1', 's 1');
    expect(api.last.path).toBe('/api/v1/traces/t%2F1/spans/s%201/replay');
    expect(api.last.body).toEqual({ variants: [], reason: '' });
  });
});

describe('replayRun', () => {
  it('is frozen by default and unsigned', async () => {
    api.reply('POST', '/api/v1/traces/t1/replay', { status: 'completed', steps: [] });
    await pro.replayRun('t1', 's2', { edits: { model: 'claude-haiku' }, reason: 'check' });
    expect(api.last.body).toEqual({ from_span: 's2', edits: { model: 'claude-haiku' }, tools: 'frozen', live_tools: [], reason: 'check' });
    expect(api.last.headers['x-signing-token']).toBeUndefined();
    await pro.replayRun('t1', 's2', { tools: 'blocked' });
    expect(api.last.body.tools).toBe('blocked');
  });

  it('checks the modes before sending', async () => {
    await expect(pro.replayRun('t1', 's2', { tools: 'simulated' as never })).rejects.toThrow(/frozen, blocked, live/);
    await expect(pro.replayRun('t1', 's2', { tools: 'live' })).rejects.toThrow(/liveTools/);
    await expect(pro.replayRun('t1', 's2', { tools: 'frozen', liveTools: ['lookup'] })).rejects.toThrow(/only go with/);
    await expect(pro.replayRun('t1', 's2', { tools: 'live', liveTools: ['lookup'], password: 'pw', signingToken: 't' })).rejects.toThrow(/not both/);
    expect(api.requests).toHaveLength(0);
  });

  it('prepare returns what to sign', async () => {
    api.reply('POST', '/api/v1/traces/t1/replay/prepare', { gate: 'live_tool_replay', record_id: 't1:s2', payload: { digest: 'x' } });
    const prep = await pro.prepareLiveReplay('t1', 's2', ['lookup'], { edits: { temperature: 0 }, reason: 'reason' });
    expect(prep.record_id).toBe('t1:s2');
    expect(api.last.body).toEqual({ from_span: 's2', edits: { temperature: 0 }, live_tools: ['lookup'], reason: 'reason' });
  });

  it('a live replay asks the server what to sign, signs exactly that, and sends the token', async () => {
    api.reply('POST', '/api/v1/traces/t1/replay/prepare', { gate: 'live_tool_replay', record_id: 't1:s2', payload: { edits_digest: 'abc', tools: ['lookup'] } });
    api.reply('POST', '/api/v1/signatures/challenge', { subject: { digest: DIGEST }, reauth: 'password' });
    api.reply('POST', '/api/v1/signatures/token', { signing_token: 'tok.live' });
    api.reply('POST', '/api/v1/traces/t1/replay', { status: 'completed' });

    await pro.replayRun('t1', 's2', { edits: { temperature: 0 }, tools: 'live', liveTools: ['lookup'], reason: 'r', password: 'pw' });

    const [prepare, challenge, token, run] = api.requests;
    expect([prepare.path, challenge.path, token.path, run.path]).toEqual([
      '/api/v1/traces/t1/replay/prepare', '/api/v1/signatures/challenge', '/api/v1/signatures/token', '/api/v1/traces/t1/replay',
    ]);
    expect(challenge.body).toEqual({ gate: 'live_tool_replay', record_id: 't1:s2', meaning: 'approved', payload: { edits_digest: 'abc', tools: ['lookup'] } });
    expect(token.body).toMatchObject({ password: 'pw', digest: DIGEST });
    expect(run.headers['x-signing-token']).toBe('tok.live');
    expect(run.body).toMatchObject({ tools: 'live', live_tools: ['lookup'] });
    expect(run.body.password).toBeUndefined();
  });

  it('a token you hold signs nothing, and an unsigned live replay is left to the server to refuse', async () => {
    api.reply('POST', '/api/v1/traces/t1/replay', { status: 'completed' });
    await pro.replayRun('t1', 's2', { tools: 'live', liveTools: ['lookup'], signingToken: 'held' });
    expect(api.requests).toHaveLength(1);
    expect(api.last.headers['x-signing-token']).toBe('held');

    api.reply('POST', '/api/v1/traces/t1/replay', { error: 'signature_required', message: 'sign' }, 403);
    const err = await pro.replayRun('t1', 's2', { tools: 'live', liveTools: ['lookup'] }).catch((e) => e);
    expect(err).toBeInstanceOf(AgentOvenAPIError);
    expect(err.statusCode).toBe(403);
    expect(api.last.headers['x-signing-token']).toBeUndefined();
  });
});

describe('getSpanCapture', () => {
  it('reads a call whole, and a 404 says it was never captured', async () => {
    api.reply('GET', '/api/v1/traces/t1/spans/s1/capture', { trace_id: 't1', span_id: 's1', request: { messages: [] }, response: { content: 'ok' } });
    expect(((await pro.getSpanCapture('t1', 's1')).response as { content: string }).content).toBe('ok');
    api.reply('GET', '/api/v1/traces/t1/spans/old/capture', { error: 'not_captured', message: 'no capture' }, 404);
    const err = await pro.getSpanCapture('t1', 'old').catch((e) => e);
    expect(err.statusCode).toBe(404);
  });
});
