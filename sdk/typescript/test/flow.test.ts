import { readFileSync } from 'node:fs';
import { afterAll, beforeAll, beforeEach, describe, expect, it } from 'vitest';
import { Flow, FlowError, flow, type FlowNode } from '../src/index.js';
import { ProClient, AgentOvenAPIError } from '../src/pro-client.js';
import { FakeControlPlane } from './fake-server.js';

const { agent, ask, check, decide, each, lookup, mentions, notify, subRecipe, together, wait } = flow;

interface Spec {
  type: 'agent' | 'check' | 'lookup' | 'decide' | 'ask' | 'together' | 'sub_recipe' | 'wait' | 'notify';
  title?: string; agent?: string; retries?: number; timeout_secs?: number; top_k?: number;
  mentions?: string[]; when?: string; yes?: Spec[]; no?: Spec[];
  within?: '1 hour' | '4 hours' | '1 day' | '3 days' | 'no limit'; notify?: string; domain?: string; emails?: string[];
  approved?: Spec[]; rejected?: Spec[]; if_nobody_answers?: 'stop' | 'reject'; lanes?: Spec[][];
  recipe?: string; input?: Record<string, string>; output?: Record<string, string>;
  duration?: string; until?: string;
  message?: string; subject?: string; channels?: string[]; tools?: string[]; on_failure?: 'stop' | 'carry_on'; payload?: Record<string, unknown>;
}
const FIXTURES = JSON.parse(readFileSync(new URL('../../fixtures/recipe-flows.json', import.meta.url), 'utf8')) as {
  flows: { name: string; description: string; spec: Spec[]; wire: unknown[] }[];
  invalid: { name: string; spec: Spec[]; expect: string }[];
};

/** A fixture's neutral spec, written with this SDK's builders. */
function build(t: Spec): FlowNode {
  const list = (items?: Spec[]) => (items ?? []).map(build);
  switch (t.type) {
    case 'agent': return agent(t.title!, t.agent!, { retries: t.retries, timeoutSecs: t.timeout_secs });
    case 'check': return check(t.title!, t.agent!);
    case 'lookup': return t.top_k !== undefined ? lookup(t.title!, t.top_k) : lookup(t.title!);
    case 'decide': return decide(t.title!, t.when ?? (t.mentions ? mentions(...t.mentions) : ''), { yes: list(t.yes), no: list(t.no) });
    case 'ask':
      return ask(t.title!, {
        within: t.within, notify: t.notify, domain: t.domain, emails: t.emails,
        approved: t.approved ? list(t.approved) : undefined,
        rejected: t.rejected ? list(t.rejected) : undefined,
        ifNobodyAnswers: t.if_nobody_answers,
      });
    case 'together': return together(...(t.lanes ?? []).map(list));
    case 'notify': return notify(t.title!, t.message!, { channels: t.channels, tools: t.tools, subject: t.subject, payload: t.payload, onFailure: t.on_failure });
    case 'wait': return wait(t.title!, { duration: t.duration, until: t.until });
    case 'sub_recipe': return subRecipe(t.title!, t.recipe!, { inputMapping: t.input, outputMapping: t.output });
  }
}

describe('same steps as the dashboard', () => {
  for (const fixture of FIXTURES.flows) {
    it(`${fixture.name} compiles to what the dashboard writes`, () => {
      const f = new Flow(fixture.name, fixture.description, fixture.spec.map(build));
      expect(f.toWire()).toEqual({ name: fixture.name, description: fixture.description, steps: fixture.wire });
    });
  }

  for (const bad of FIXTURES.invalid) {
    it(`refuses: ${bad.name}`, () => {
      const f = new Flow('bad', '', bad.spec.map(build));
      expect(() => f.toWire()).toThrow(FlowError);
      expect(() => f.toWire()).toThrow(bad.expect);
      expect(f.validate().some((p) => p.message.includes(bad.expect))).toBe(true);
    });
  }

  it('compiling twice gives the same steps', () => {
    const fixture = FIXTURES.flows.find((f) => f.name === 'nested')!;
    const f = new Flow('n', '', fixture.spec.map(build));
    expect(f.toSteps()).toEqual(f.toSteps());
    expect(f.toSteps()).toEqual(fixture.wire);
  });
});

describe('writing a flow', () => {
  it('adds steps one at a time and chains', () => {
    const f = new Flow('two').add(agent('A', 'a')).add(agent('B', 'b'), check('C', 'c'));
    expect(f.toSteps().map((s) => s.name)).toEqual(['a', 'b', 'c']);
    expect(f.toSteps().map((s) => s.depends_on)).toEqual([[], ['a'], ['b']]);
  });

  it('sends the version only when given', () => {
    expect(new Flow('v', '', [agent('A', 'a')]).toWire()).not.toHaveProperty('version');
    expect(new Flow('v', '', [agent('A', 'a')], '1.2.0').toWire().version).toBe('1.2.0');
  });

  it('mentions() cleans the words like the dashboard', () => {
    expect(mentions(' Refund ', 'charge"back', '', 'a\\b')).toBe(
      'lower(text) contains "refund" or lower(text) contains "chargeback" or lower(text) contains "ab"',
    );
  });

  it('an agent step can loop, and a list can be gone over', () => {
    const steps = new Flow('x', '', [
      agent('Refine', 'w', { loopWhile: 'output.done == false', maxIterations: 5 }),
      each('Per item', 'w', 'refine.items', { maxConcurrency: 3 }),
      subRecipe('Then', 'other', { inputMapping: { a: 'b' } }),
    ]).toSteps();
    expect(steps[0]).toMatchObject({ loop_condition: 'output.done == false', max_iterations: 5 });
    expect(steps[1]).toMatchObject({ kind: 'map', source_path: 'refine.items', max_concurrency: 3 });
    expect(steps[2]).toMatchObject({ kind: 'sub_recipe', recipe_ref: 'other', input_mapping: { a: 'b' } });
    expect(new Flow('y', '', [agent('A', 'a')]).toSteps()[0]).not.toHaveProperty('loop_condition');
  });

  it('only accepts steps made with the builders', () => {
    expect(() => new Flow('bad', '', ['x' as unknown as FlowNode])).toThrow(TypeError);
    expect(() => decide('d', 'x', { yes: [undefined as unknown as FlowNode] })).toThrow(TypeError);
  });

  it('together() needs two to four lanes', () => {
    expect(() => together([agent('A', 'a')])).toThrow('2 to 4');
    expect(() => together(...[0, 1, 2, 3, 4].map((i) => [agent(`S${i}`, 'a')]))).toThrow('2 to 4');
    expect(together(...[0, 1, 2, 3].map((i) => [agent(`S${i}`, 'a')])).lanes).toHaveLength(4);
  });

  it('ask() checks its arguments', () => {
    expect(() => ask('x', { within: '2 weeks' as never })).toThrow('within');
    expect(ask('x', { minutes: 45 }).config.max_wait_minutes).toBe(45);
    expect(() => ask('x', { ifNobodyAnswers: 'reject' })).toThrow('Rejected path');
    expect(() => ask('x', { ifNobodyAnswers: 'wait' as never, approved: [] })).toThrow('stop');
    expect(ask('x').config).not.toHaveProperty('on_reject');
    expect(ask('x', { rejected: [] }).config.on_reject).toBe('branch');
  });
});

describe('notify', () => {
  it('carries where and what it sends', () => {
    const n = notify('Tell', 'Run {{run_id}} needs you', { channels: ['ops'], tools: ['pager'], subject: 'Approval', payload: { priority: 'high' }, onFailure: 'carry_on' });
    expect(n.config).toEqual({ message: 'Run {{run_id}} needs you', subject: 'Approval', channels: ['ops'], on_failure: 'carry_on', payload: { priority: 'high' } });
    expect(n.fields).toEqual({ notify_tools: ['pager'] });
    expect(notify('Tell', 'hi', { channels: ['ops'] }).config).toEqual({ message: 'hi', channels: ['ops'] });
    expect(() => notify('Tell', 'hi', { channels: ['ops'], onFailure: 'shrug' as never })).toThrow('carry_on');
  });

  it('is checked before it is sent', () => {
    expect(new Flow('f', '', [notify('Tell', 'hi')]).validate().map((p) => p.message)).toEqual(['"Tell" needs a channel or a tool to send to.']);
    expect(new Flow('f', '', [notify('Tell', ' ', { channels: ['ops'] })]).validate().map((p) => p.message)).toEqual(['"Tell" needs a message.']);
    expect(new Flow('f', '', [notify('Tell', '')]).validate()).toHaveLength(2);
    expect(new Flow('f', '', [notify('Tell', 'hi', { tools: ['pager'] })]).validate()).toEqual([]);
  });
});

describe('wait', () => {
  it('is written as the engine reads it', () => {
    const cases: [string | number, string][] = [['30s', '30s'], ['5M', '5m'], ['1.5h', '1.5h'], [90, '90s'], [3600, '1h'], [86400, '1d'], [120, '2m'], [0.5, '0.5s'], ['90', '90s']];
    for (const [given, written] of cases) expect(wait('w', { duration: given }).config).toEqual({ duration: written });
  });

  it('until takes a Date or ISO text', () => {
    expect(wait('w', { until: new Date('2026-11-02T09:30:15.999Z') }).config).toEqual({ until: '2026-11-02T09:30:15Z' });
    expect(wait('w', { until: '2026-11-02T09:00:00Z' }).config).toEqual({ until: '2026-11-02T09:00:00Z' });
    expect(() => wait('w', { until: 'tomorrow' })).toThrow('ISO 8601');
  });

  it('refuses what cannot be read', () => {
    for (const bad of ['soon', '5 minutes', '-3m', '1w', '', -5]) expect(() => wait('w', { duration: bad })).toThrow(RangeError);
    expect(() => wait('w', { duration: '5m', until: '2026-11-02T09:00:00Z' })).toThrow('not both');
  });

  it('is checked before it is sent', () => {
    expect(new Flow('f', '', [wait('Pause')]).validate().map((p) => p.message)).toEqual(['"Pause" needs a time to wait.']);
    expect(new Flow('f', '', [wait('Pause', { duration: '31d' })]).validate().map((p) => p.message)).toEqual(['"Pause" would wait longer than 30 days.']);
    expect(new Flow('f', '', [wait('Pause', { duration: '30d' })]).validate()).toEqual([]);
  });
});

describe('checking a flow', () => {
  it('lists every problem together', () => {
    const f = new Flow('bad', '', [agent('One', ''), decide('Pick', '')]);
    try {
      f.toWire();
      throw new Error('should have thrown');
    } catch (e) {
      expect(e).toBeInstanceOf(FlowError);
      expect((e as FlowError).problems.map((p) => p.message)).toEqual(['"One" needs an agent.', '"Pick" needs a condition to test.']);
    }
    expect(f.toWire({ validate: false }).steps.length).toBe(4); // validation can be skipped on purpose (a decision with empty paths adds two carry-on steps)
  });

  it('checks agents against the names that exist', () => {
    const f = new Flow('f', '', [agent('A', 'ghost'), check('B', 'real')]);
    expect(f.validate()).toEqual([]);
    expect(f.validate(['real']).map((p) => p.message)).toEqual(['"A" uses ghost, which does not exist.']);
    expect(() => f.toWire({ agents: ['real'] })).toThrow(FlowError);
  });

  it('a map and a sub-recipe need what they run', () => {
    expect(new Flow('f', '', [each('E', 'a', ''), subRecipe('S', '')]).validate().map((p) => p.message)).toEqual([
      '"E" needs the list to go over.', '"S" needs a recipe to run.',
    ]);
  });
});

describe('the client', () => {
  const api = new FakeControlPlane();
  let pro: ProClient;
  beforeAll(async () => { await api.start(); });
  afterAll(async () => { await api.stop(); });
  beforeEach(() => {
    api.requests = [];
    pro = new ProClient({ url: api.url, apiKey: 'ao_key', kitchen: 'payments' });
  });

  const FLOW = new Flow('refund-review', 'Ask about refunds', [
    agent('Triage', 'triage'),
    ask('Sign off', { approved: [agent('Send it', 'sender')], rejected: [agent('Revise', 'writer')] }),
  ]);

  it('createRecipe sends the flow as the dashboard would', async () => {
    api.reply('POST', '/api/v1/recipes', { name: 'refund-review', steps: [] }, 201);
    const created = await pro.createRecipe(FLOW);
    expect(created.name).toBe('refund-review');
    expect(api.last.headers['authorization']).toBe('Bearer ao_key');
    expect(api.last.headers['x-kitchen']).toBe('payments');
    expect(api.last.body).toEqual(FLOW.toWire());
    expect(api.last.body.steps[1].config.on_reject).toBe('branch');
  });

  it('createRecipe can check the agents exist', async () => {
    api.reply('GET', '/api/v1/agents', [{ name: 'triage' }, { name: 'sender' }]);
    api.reply('POST', '/api/v1/recipes', { name: 'refund-review' }, 201);
    await expect(pro.createRecipe(FLOW, { checkAgents: true })).rejects.toThrow('"Revise" uses writer, which does not exist');
    expect(api.requests.map((r) => r.method)).toEqual(['GET']);
    api.reply('GET', '/api/v1/agents', [{ name: 'triage' }, { name: 'sender' }, { name: 'writer' }]);
    await pro.createRecipe(FLOW, { checkAgents: true });
    expect(api.last.method).toBe('POST');
  });

  it('a flow that is wrong never reaches the server', async () => {
    await expect(pro.createRecipe(new Flow('empty'))).rejects.toThrow(FlowError);
    expect(api.requests).toEqual([]);
  });

  it('a hand-written recipe still goes through unchanged', async () => {
    api.reply('POST', '/api/v1/recipes', { name: 'raw' }, 201);
    await pro.createRecipe({ name: 'raw', steps: [{ name: 'a', kind: 'agent', agent_ref: 'x' }] });
    expect(api.last.body).toEqual({ name: 'raw', steps: [{ name: 'a', kind: 'agent', agent_ref: 'x' }] });
  });

  it('restoreRecipe and rerunRecipeRun', async () => {
    api.reply('POST', '/api/v1/recipes/refund-review/restore', { name: 'refund-review' });
    expect((await pro.restoreRecipe('refund-review')).name).toBe('refund-review');
    api.reply('POST', '/api/v1/recipes/refund-review/runs/r1/rerun', { run_id: 'r2', rerun_of: 'r1', status: 'running' }, 202);
    expect(await pro.rerunRecipeRun('refund-review', 'r1')).toEqual({ run_id: 'r2', rerun_of: 'r1', status: 'running' });
    expect(api.last.body).toEqual({});
    await pro.rerunRecipeRun('refund-review', 'r1', { text: 'other' });
    expect(api.last.body).toEqual({ input: { text: 'other' } });
  });

  it('a server refusal is an API error', async () => {
    api.reply('POST', '/api/v1/recipes', { error: 'recipe exists' }, 409);
    await expect(pro.createRecipe(FLOW)).rejects.toBeInstanceOf(AgentOvenAPIError);
  });

  it('updateRecipe replaces description and steps', async () => {
    api.reply('PUT', '/api/v1/recipes/refund-review', { name: 'refund-review' });
    await pro.updateRecipe(FLOW);
    expect(api.last.body).toEqual({ description: 'Ask about refunds', steps: FLOW.toSteps() });
  });

  it('runRecipe starts a run with the input', async () => {
    api.reply('POST', '/api/v1/recipes/refund-review/bake', { run_id: 'r1', status: 'running' });
    expect((await pro.runRecipe('refund-review', { text: 'refund please' })).run_id).toBe('r1');
    expect(api.last.body).toEqual({ input: { text: 'refund please' } });
    await pro.runRecipe('refund-review', {}, { environment: 'staging' });
    expect(api.last.body).toEqual({ input: {}, environment: 'staging' });
  });

  it('waiting stops at a gate and returns what is waiting', async () => {
    api.reply('GET', '/api/v1/recipes/refund-review/runs/r1', { run: { id: 'r1', status: 'paused' }, pending_gates: ['sign-off'] });
    const run = await pro.waitForRecipeRun('refund-review', 'r1', { pollMs: 10 });
    expect(run.status).toBe('paused');
    expect(run.pending_gates).toEqual(['sign-off']);
  });

  it('waiting returns a finished run and times out otherwise', async () => {
    api.reply('GET', '/api/v1/recipes/r/runs/done', { run: { id: 'done', status: 'completed', output: { x: 1 } } });
    expect((await pro.waitForRecipeRun('r', 'done')).output).toEqual({ x: 1 });
    api.reply('GET', '/api/v1/recipes/r/runs/slow', { run: { id: 'slow', status: 'running' } });
    await expect(pro.waitForRecipeRun('r', 'slow', { timeoutMs: 50, pollMs: 10 })).rejects.toThrow("still 'running'");
    api.reply('GET', '/api/v1/recipes/r/runs/gate', { run: { id: 'gate', status: 'paused' }, pending_gates: ['g'] });
    await expect(pro.waitForRecipeRun('r', 'gate', { timeoutMs: 50, pollMs: 10, stopAtGate: false })).rejects.toThrow('still');
  });

  it('manages channels and hands out tickets', async () => {
    api.reply('POST', '/api/v1/channels', { name: 'ops', kind: 'websocket' }, 201);
    api.reply('GET', '/api/v1/channels', [{ name: 'ops' }]);
    api.reply('DELETE', '/api/v1/channels/ops', {});
    api.reply('POST', '/api/v1/channels/ops/ws-ticket', { token: 't.k', expires_in: 60, subprotocol: 'agentoven-token.t.k' });
    await pro.createChannel({ name: 'ops' });
    expect(api.last.body).toEqual({ kind: 'websocket', events: [], name: 'ops' });
    await pro.createChannel({ name: 'hook', kind: 'webhook', url: 'https://x.example/h', secret: 's', events: ['gate_waiting'] });
    expect(api.last.body).toEqual({ kind: 'webhook', events: ['gate_waiting'], name: 'hook', url: 'https://x.example/h', secret: 's' });
    expect(await pro.listChannels()).toEqual([{ name: 'ops' }]);
    expect((await pro.channelTicket('ops')).token).toBe('t.k');
    await pro.deleteChannel('ops');
    expect(api.last.method).toBe('DELETE');
  });

  it('releaseWait ends a wait early', async () => {
    api.reply('POST', '/api/v1/recipes/r/runs/run1/steps/pause/release', { released: true });
    await pro.releaseWait('r', 'run1', 'pause');
    expect(api.last.method).toBe('POST');
    api.reply('POST', '/api/v1/recipes/r/runs/run2/steps/pause/release', { error: 'not waiting' }, 404);
    await expect(pro.releaseWait('r', 'run2', 'pause')).rejects.toBeInstanceOf(AgentOvenAPIError);
  });

  it('approving and rejecting record the decision', async () => {
    api.reply('POST', '/api/v1/approvals/r1/sign-off', { approved: true });
    await pro.approveGate('r1', 'sign-off');
    expect(api.last.body).toEqual({ approved: true });
    await pro.rejectGate('r1', 'sign-off', 'tone is too casual');
    expect(api.last.body).toEqual({ approved: false, comments: 'tone is too casual' });
    await pro.approveGate('r1', 'sign-off', 'ok');
    expect(api.last.body).toEqual({ approved: true, comments: 'ok' });
  });
});
