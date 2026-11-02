import { afterAll, beforeAll, beforeEach, describe, expect, it } from 'vitest';
import { Tasks, parseSse } from '../src/tasks.js';
import { AgentOvenAPIError } from '../src/pro-client.js';
import { FakeControlPlane } from './fake-server.js';

const api = new FakeControlPlane();
let tasks: Tasks;
const DIGEST = 'd'.repeat(64);
const PAUSED = { id: 't1', agent: 'researcher', status: 'paused', pause_kind: 'budget', turns: 7 };

beforeAll(async () => {
  await api.start();
});
afterAll(async () => {
  await api.stop();
});
beforeEach(() => {
  api.requests = [];
  tasks = new Tasks({ url: api.url, apiKey: 'ao_key', kitchen: 'acme' });
  api.reply('POST', '/api/v1/signatures/challenge', { subject: { digest: DIGEST }, reauth: 'password' });
  api.reply('POST', '/api/v1/signatures/token', { signing_token: 'tok.sig' });
  api.reply('POST', '/api/v1/tasks/t1/resume', { id: 't1', status: 'queued' }, 202);
  api.reply('GET', '/api/v1/tasks/t1', PAUSED);
});

describe('tasks', () => {
  it('start sends only the caps that were given, in the active kitchen', async () => {
    api.reply('POST', '/api/v1/tasks', { id: 't1', status: 'queued', agent: 'researcher' }, 202);
    const out = await tasks.start('researcher', 'find the cause', { maxCostUsd: 5, maxTurns: 40, metadata: { ticket: 'OPS-1' }, sessionId: 's1' });
    expect(out.id).toBe('t1');
    expect(api.last.headers['x-kitchen']).toBe('acme');
    expect(api.last.headers['authorization']).toBe('Bearer ao_key');
    expect(api.last.body).toEqual({ agent: 'researcher', message: 'find the cause', session_id: 's1', budget: { max_cost_usd: 5, max_turns: 40 }, metadata: { ticket: 'OPS-1' } });
    await tasks.start('researcher', 'go');
    expect(api.last.body).toEqual({ agent: 'researcher', message: 'go' });
  });

  it('refuses before sending what the server would refuse', () => {
    expect(() => tasks.start('a', '  ')).toThrow('needs a message');
    expect(() => tasks.start('a', 'go', { maxCostUsd: 0 })).toThrow('more than zero');
    expect(() => tasks.start('a', 'go', { maxTokens: -1 })).toThrow('more than zero');
    expect(api.requests).toHaveLength(0);
  });

  it('lists, gets and cancels', async () => {
    api.reply('GET', '/api/v1/tasks', { count: 1, tasks: [PAUSED] });
    api.reply('GET', '/api/v1/tasks?status=paused', { count: 1, tasks: [PAUSED] });
    api.reply('POST', '/api/v1/tasks/t1/cancel', { id: 't1', status: 'cancelled' });
    expect((await tasks.list())[0].id).toBe('t1');
    expect((await tasks.list('paused'))[0].status).toBe('paused');
    expect((await tasks.get('t1')).pause_kind).toBe('budget');
    expect((await tasks.cancel('t1')).status).toBe('cancelled');
  });

  it('a missing task is an AgentOvenAPIError 404', async () => {
    api.reply('GET', '/api/v1/tasks/t9', { error: 'task not found' }, 404);
    await expect(tasks.get('t9')).rejects.toMatchObject({ statusCode: 404 });
    await expect(tasks.get('t9')).rejects.toBeInstanceOf(AgentOvenAPIError);
  });

  it('resume with nothing sends an empty body for an interrupted task', async () => {
    await tasks.resume('t1');
    expect(api.last.body).toEqual({});
  });

  it('resume extends a budget, with an id so a retry applies once', async () => {
    await tasks.resume('t1', { extend: { add_cost_usd: 5, add_tokens: 0, reason: 'close to done' } });
    expect(api.last.body.approved).toBe(true);
    expect(api.last.body.extend).toMatchObject({ add_cost_usd: 5, reason: 'close to done' });
    expect(api.last.body.extend.add_tokens).toBeUndefined();
    expect(api.last.body.extend.id).toHaveLength(36);
    expect(api.last.headers['x-signing-token']).toBeUndefined();
  });

  it('resume refuses nonsense before sending', async () => {
    api.requests = [];
    await expect(tasks.resume('t1', { extend: { reason: 'why' } })).rejects.toThrow('adds something');
    await expect(tasks.resume('t1', { extend: { add_cost_usd: -1 } })).rejects.toThrow('more than zero');
    await expect(tasks.resume('t1', { approved: false, extend: { add_turns: 5 } })).rejects.toThrow('denial');
    await expect(tasks.resume('t1', { password: 'pw', signingToken: 't' })).rejects.toThrow('not both');
    expect(api.requests).toHaveLength(0);
  });

  it('a signed resume signs for the gate the pause waits at, and what is signed is what is sent', async () => {
    await tasks.resume('t1', { extend: { add_tokens: 50000 }, password: 'pw' });
    const challenge = api.requests.find((r) => r.path === '/api/v1/signatures/challenge')!;
    expect(challenge.body).toMatchObject({ gate: 'budget_extension', record_id: 't1', meaning: 'approved' });
    expect(api.last.headers['x-signing-token']).toBe('tok.sig');
    expect(api.last.body.extend).toEqual(challenge.body.payload.extend);
  });

  it('a denial is signed as a rejection; a held token is sent as is', async () => {
    api.reply('GET', '/api/v1/tasks/t1', { ...PAUSED, pause_kind: 'approval' });
    await tasks.resume('t1', { approved: false, note: 'not that tool', password: 'pw' });
    const challenge = api.requests.find((r) => r.path === '/api/v1/signatures/challenge')!;
    expect(challenge.body).toMatchObject({ gate: 'tool_approval', meaning: 'rejected', payload: { note: 'not that tool' } });
    api.requests = [];
    await tasks.resume('t1', { approved: true, signingToken: 'held.tok' });
    expect(api.requests.map((r) => r.path)).toEqual(['/api/v1/tasks/t1/resume']);
    expect(api.last.headers['x-signing-token']).toBe('held.tok');
  });

  it('refuses to sign a task that is not waiting', async () => {
    api.reply('GET', '/api/v1/tasks/t1', { id: 't1', status: 'running' });
    await expect(tasks.resume('t1', { approved: true, password: 'pw' })).rejects.toThrow('nothing to sign');
  });

  it('wait returns when the task stops for a person, and times out on one that keeps running', async () => {
    expect((await tasks.wait('t1', { pollMs: 1 })).status).toBe('paused');
    api.reply('GET', '/api/v1/tasks/t2', { id: 't2', status: 'running' });
    await expect(tasks.wait('t2', { timeoutMs: 50, pollMs: 10 })).rejects.toThrow('still running');
  });

  it('events follow the stream from where a client left off', async () => {
    const ev = (seq: number, type: string, extra: object) => `id: ${seq}\nevent: ${type}\ndata: ${JSON.stringify({ seq, event: { type, ...extra } })}\n\n`;
    api.replyRaw('GET', '/api/v1/tasks/t1/events?after=3', ev(4, 'task_status', { content: 'running' }) + ': ping\n\n' + ev(5, 'done', { content: 'ok' }));
    const got = [];
    for await (const e of tasks.events('t1', 3)) got.push(e);
    expect(got.map((e) => [e.seq, e.type])).toEqual([[4, 'task_status'], [5, 'done']]);
    expect(got[1].event.content).toBe('ok');
    expect(api.last.headers['accept']).toBe('text/event-stream');
    expect(api.last.headers['x-kitchen']).toBe('acme');
  });

  it('the event stream of a missing task is an API error', async () => {
    api.reply('GET', '/api/v1/tasks/t9/events', { error: 'task not found' }, 404);
    await expect((async () => { for await (const _ of tasks.events('t9')) { /* none */ } })()).rejects.toMatchObject({ statusCode: 404 });
  });

  it('the SSE parser handles chunk boundaries, joins data lines and skips pings', async () => {
    async function* chunks() {
      yield 'id: 1\nevent: task_st';
      yield 'atus\ndata: {"a":\ndata: 1}\n\n: pi';
      yield 'ng\n\nid: 2\ndata: x\n';
    }
    const got = [];
    for await (const e of parseSse(chunks())) got.push(e);
    expect(got).toEqual([{ id: 1, event: 'task_status', data: '{"a":\n1}' }, { id: 2, event: '', data: 'x' }]);
  });
});
