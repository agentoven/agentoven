/// <reference types="node" />
// Run with `npm test` (Node's built-in runner). The flow helpers are pure, so they need no DOM.
import assert from 'node:assert/strict';
import { test } from 'node:test';
import {
  APPROVED_CONDITION, CARRY_ON, TEMPLATES, YES_CONDITION, canRemovePaths, setApprovalPaths, mentionsExpression, resizeLanes, wordsFromExpression, allNodes, compile, decompile, findNode, insertNode, newStep, removeNode, sameWire,
  slug, stepCount, titleOf, uniqueName, updateStep, validate, whereAfter, type FlowNode, type WireStep,
} from './recipeFlow.ts';

const agents = new Set(['triage', 'writer']);
const step = (title: string, kind: string, extra: Partial<WireStep> = {}): FlowNode => ({
  id: title, step: { name: slug(title), kind, ...extra, config: { title, ...(extra.config ?? {}) } },
});
const agent = (title: string, ref = 'triage') => step(title, 'agent', { agent_ref: ref });
const decide = (title: string, yes: FlowNode[], no: FlowNode[]): FlowNode => ({
  id: title, step: { name: slug(title), kind: 'condition', config: { title, expression: 'amount > 200' } }, yes, no,
});
const byName = (steps: WireStep[], name: string): WireStep => steps.find((s) => s.name === name)!;
const rewrap = (steps: WireStep[]) => decompile(JSON.parse(JSON.stringify(steps)) as WireStep[]);

test('slug and uniqueName make safe, distinct names', () => {
  assert.equal(slug('Ask a person to approve!'), 'ask-a-person-to-approve');
  assert.equal(slug('???'), 'step');
  assert.equal(uniqueName('Draft', new Set(['draft', 'draft-2'])), 'draft-3');
});

test('a chain compiles to steps that each wait for the one before', () => {
  const steps = compile([agent('Triage'), agent('Write', 'writer')]);
  assert.deepEqual(steps.map((s) => [s.name, s.depends_on]), [['triage', []], ['write', ['triage']]]);
  assert.equal(byName(steps, 'write').agent_ref, 'writer');
  assert.equal(byName(steps, 'write').agent, 'writer', 'the older alias is kept');
});

test('a decision compiles to a condition step with a Yes branch and a default', () => {
  const steps = compile([agent('Triage'), decide('Over limit', [step('Ask', 'human_gate')], [agent('Refund', 'writer')]), agent('Reply', 'writer')]);
  const d = byName(steps, 'over-limit');
  assert.deepEqual(d.branches, [{ condition: YES_CONDITION, next_step: 'ask' }]);
  assert.equal(d.default_next, 'refund');
  assert.deepEqual(byName(steps, 'ask').depends_on, ['over-limit']);
  assert.deepEqual(byName(steps, 'refund').depends_on, ['over-limit']);
  assert.deepEqual([...byName(steps, 'reply').depends_on!].sort(), ['ask', 'refund']);
});

test('an empty path becomes a carry-on step, so the engine still skips the other path', () => {
  const steps = compile([decide('Over limit', [step('Ask', 'human_gate')], [])]);
  const pass = byName(steps, 'over-limit-otherwise');
  assert.equal(pass.kind, 'fan_out');
  assert.equal(pass.config?.ui, CARRY_ON);
  assert.equal(byName(steps, 'over-limit').default_next, 'over-limit-otherwise');
});

test('flows survive being written and read back', () => {
  const flows: FlowNode[][] = [
    [],
    [agent('Only')],
    [agent('A'), agent('B', 'writer'), step('Look', 'rag', { config: { top_k: 3 } })],
    [agent('Triage'), decide('Over limit', [step('Ask', 'human_gate'), agent('Process', 'writer')], [agent('Auto', 'writer')]), agent('Reply', 'writer')],
    [decide('First', [agent('A')], []), agent('After')],
    [decide('Outer', [decide('Inner', [agent('X')], [agent('Y')])], [agent('Z')]), agent('Done')],
    [agent('Start'), decide('Last', [agent('Yes')], [agent('No')])],
  ];
  for (const flow of flows) {
    const steps = compile(flow);
    const back = rewrap(steps);
    assert.ok(back.ok, `could not read back ${JSON.stringify(steps.map((s) => s.name))}: ${back.ok ? '' : back.reason}`);
    assert.ok(sameWire(compile(back.flow), steps), 'writes back unchanged');
    assert.equal(stepCount(back.flow), stepCount(flow));
  }
});

test('the templates are valid, drawable flows', () => {
  for (const t of TEMPLATES) {
    const flow = t.build('triage');
    assert.deepEqual(validate(flow, agents), [], t.name);
    assert.ok(rewrap(compile(flow)).ok, t.name);
  }
});

test('settings the builder does not manage are kept', () => {
  const flow = [agent('Triage')];
  flow[0].step.max_retries = 3;
  flow[0].step.timeout_secs = 90;
  flow[0].step.notify_tools = ['slack'];
  const back = rewrap(compile(flow));
  assert.ok(back.ok);
  assert.equal(back.flow[0].step.max_retries, 3);
  assert.equal(back.flow[0].step.timeout_secs, 90);
  assert.deepEqual(back.flow[0].step.notify_tools, ['slack']);
});

test('a recipe written by hand with only the agent alias is readable', () => {
  const wire: WireStep[] = [
    { name: 'a', kind: 'agent', agent: 'triage' },
    { name: 'b', kind: 'agent', agent: 'writer', depends_on: ['a'] },
  ];
  const back = decompile(wire);
  assert.ok(back.ok);
  assert.equal(back.flow[0].step.agent, 'triage');
});

test('shapes the builder cannot draw are refused, not rewritten', () => {
  const map: WireStep[] = [{ name: 'a', kind: 'agent', agent: 'x' }, { name: 'm', kind: 'map', depends_on: ['a'] }];
  const m = decompile(map);
  assert.equal(m.ok, false);
  assert.match(m.ok ? '' : m.reason, /"map" step/);
  const missing: WireStep[] = [{ name: 'a', kind: 'agent', agent: 'x', depends_on: ['ghost'] }];
  assert.equal(decompile(missing).ok, false);
  const loop: WireStep[] = [{ name: 'a', kind: 'agent', agent: 'x', depends_on: ['b'] }, { name: 'b', kind: 'agent', agent: 'x', depends_on: ['a'] }];
  assert.equal(decompile(loop).ok, false, 'nothing can start');
  const split: WireStep[] = [
    { name: 'a', kind: 'agent', agent: 'x' },
    { name: 'b', kind: 'agent', agent: 'x', depends_on: ['a'] },
    { name: 'c', kind: 'agent', agent: 'x', depends_on: ['a'] },
    { name: 'd', kind: 'agent', agent: 'x', depends_on: ['b'] },
    { name: 'e', kind: 'agent', agent: 'x', depends_on: ['c', 'd'] },
  ];
  const ok = decompile(split);
  assert.ok(ok.ok, 'lanes of different lengths that meet again are fine');
});

test('steps with nothing before them start together', () => {
  const two: WireStep[] = [{ name: 'a', kind: 'agent', agent: 'x' }, { name: 'b', kind: 'agent', agent: 'x' }];
  const r = decompile(two);
  assert.ok(r.ok);
  assert.equal(r.flow.length, 1);
  assert.equal(r.flow[0].lanes?.length, 2);
});

test('a decision written with other branch conditions is refused', () => {
  const wire: WireStep[] = [
    { name: 'c', kind: 'condition', config: { expression: 'x' }, branches: [{ condition: 'category == "a"', next_step: 'p' }], default_next: 'q' },
    { name: 'p', kind: 'agent', agent: 'x', depends_on: ['c'] },
    { name: 'q', kind: 'agent', agent: 'x', depends_on: ['c'] },
  ];
  assert.equal(decompile(wire).ok, false);
});

test('validate names what would stop a run', () => {
  assert.equal(validate([], agents)[0].message, 'Add at least one step.');
  const flow = [agent('Triage', ''), agent('Gone', 'deleted'), decide('Choice', [], []), agent('Fine', 'writer')];
  flow[2].step.config = { title: 'Choice', expression: '  ' };
  const msgs = validate(flow, agents).map((p) => p.message);
  assert.equal(msgs.length, 3);
  assert.match(msgs[0], /needs an agent/);
  assert.match(msgs[1], /no longer exists/);
  assert.match(msgs[2], /needs a condition/);
});

test('editing helpers insert, update and remove without changing the original', () => {
  const start: FlowNode[] = [agent('A'), decide('Choice', [], [])];
  const added = newStep('person', start);
  const w = whereAfter(start, 'A')!;
  assert.deepEqual(w, { parent: null, path: null, index: 1 });
  const next = insertNode(start, w, added);
  assert.equal(start.length, 2, 'original untouched');
  assert.equal(next[1].id, added.id);
  const inYes = insertNode(next, { parent: 'Choice', path: 'yes', index: 0 }, newStep('agent', next, 'writer'));
  assert.equal(findNode(inYes, 'Choice')!.yes!.length, 1);
  assert.deepEqual(whereAfter(inYes, findNode(inYes, 'Choice')!.yes![0].id), { parent: 'Choice', path: 'yes', index: 1 });
  const renamed = updateStep(inYes, 'A', {}, { title: 'Triage the ticket' });
  assert.equal(titleOf(findNode(renamed, 'A')!.step), 'Triage the ticket');
  assert.equal(titleOf(findNode(inYes, 'A')!.step), 'A');
  const removed = removeNode(inYes, 'Choice');
  assert.equal(allNodes(removed).length, 2, 'removing a decision removes its paths');
});

test('new steps get distinct names and sensible defaults', () => {
  let flow: FlowNode[] = [];
  for (let i = 0; i < 3; i++) flow = insertNode(flow, { parent: null, path: null, index: flow.length }, newStep('agent', flow, 'triage'));
  assert.deepEqual(flow.map((n) => n.step.name), ['agent-step', 'agent-step-2', 'agent-step-3']);
  const gate = newStep('person', flow);
  assert.equal(gate.step.kind, 'human_gate');
  assert.equal(gate.step.config?.max_wait_minutes, 1440);
  const d = newStep('decide', flow);
  assert.deepEqual([d.yes, d.no], [[], []]);
});

test('a "mentions" condition is written and read back, and hand-written ones are left alone', () => {
  const expr = mentionsExpression(['Refund', ' chargeback ', '']);
  assert.equal(expr, 'lower(text) contains "refund" or lower(text) contains "chargeback"');
  assert.deepEqual(wordsFromExpression(expr), ['refund', 'chargeback']);
  assert.equal(wordsFromExpression('amount > 200'), null);
  assert.equal(wordsFromExpression(''), null);
  assert.equal(mentionsExpression(['say "hi"']), 'lower(text) contains "say hi"');
});

const parallel = (title: string, lanes: FlowNode[][]): FlowNode => ({ id: title, step: { name: '', kind: 'parallel' }, lanes });

test('steps side by side compile so each lane waits for what came before and the next step waits for all', () => {
  const steps = compile([agent('Plan'), parallel('P', [[agent('Market')], [agent('Copy', 'writer'), agent('Polish', 'writer')]]), step('Sign off', 'human_gate')]);
  assert.deepEqual(byName(steps, 'market').depends_on, ['plan']);
  assert.deepEqual(byName(steps, 'copy').depends_on, ['plan']);
  assert.deepEqual(byName(steps, 'polish').depends_on, ['copy']);
  assert.deepEqual([...byName(steps, 'sign-off').depends_on!].sort(), ['market', 'polish']);
});

test('the same recipes the dashboard already has (a plan, two side by side, a sign-off) are readable', () => {
  const wire: WireStep[] = [
    { name: 'plan', kind: 'agent', agent_ref: 'triage', agent: 'triage' },
    { name: 'analyze-market', kind: 'agent', agent_ref: 'triage', agent: 'triage', depends_on: ['plan'] },
    { name: 'write-copy', kind: 'agent', agent_ref: 'writer', agent: 'writer', depends_on: ['plan'] },
    { name: 'sign-off', kind: 'human_gate', depends_on: ['analyze-market', 'write-copy'] },
  ];
  const back = decompile(wire);
  assert.ok(back.ok, back.ok ? '' : back.reason);
  assert.equal(back.flow.length, 3);
  assert.equal(back.flow[1].lanes?.length, 2);
  assert.ok(sameWire(compile(back.flow), wire));
});

test('parallel groups survive being written and read back, alone, nested, first and last', () => {
  const flows: FlowNode[][] = [
    [agent('A'), parallel('P1', [[agent('B')], [agent('C')], [agent('D')]]), agent('E')],
    [parallel('P1', [[agent('B')], [agent('C')]]), agent('After')],
    [agent('A'), parallel('P1', [[agent('B')], [agent('C')]])],
    [agent('A'), parallel('P1', [[decide('Choice', [agent('Y')], [agent('N')])], [agent('C')]]), agent('Z')],
    [agent('A'), decide('Choice', [parallel('P1', [[agent('Y1')], [agent('Y2')]])], [agent('N')]), agent('Z')],
    [agent('A'), parallel('Outer', [[parallel('Inner', [[agent('X')], [agent('Y')]])], [agent('Z')]]), agent('End')],
  ];
  for (const flow of flows) {
    const steps = compile(flow);
    const back = rewrap(steps);
    assert.ok(back.ok, `could not read back ${JSON.stringify(steps.map((s) => [s.name, s.depends_on]))}: ${back.ok ? '' : back.reason}`);
    assert.ok(sameWire(compile(back.flow), steps), 'writes back unchanged');
    assert.equal(stepCount(back.flow), stepCount(flow));
  }
});

test('a group of steps that is not drawable is refused', () => {
  const wire: WireStep[] = [
    { name: 'a', kind: 'agent', agent: 'x' },
    { name: 'b', kind: 'agent', agent: 'x', depends_on: ['a'] },
    { name: 'c', kind: 'agent', agent: 'x', depends_on: ['a', 'b'] },
  ];
  assert.equal(decompile(wire).ok, false, 'a step that waits for its sibling');
  const five: WireStep[] = [{ name: 'a', kind: 'agent', agent: 'x' }, ...['b', 'c', 'd', 'e', 'f'].map((n) => ({ name: n, kind: 'agent', agent: 'x', depends_on: ['a'] }))];
  assert.equal(decompile(five).ok, false, 'more than four lanes');
});

test('lanes can be added, removed and filled, and a group needs two steps', () => {
  let flow: FlowNode[] = [agent('A')];
  const group = newStep('parallel', flow);
  flow = insertNode(flow, { parent: null, path: null, index: 1 }, group);
  assert.equal(group.lanes?.length, 2);
  assert.equal(validate(flow, agents).some((p) => /side by side/.test(p.message)), true);
  flow = insertNode(flow, { parent: group.id, path: 0, index: 0 }, agent('B'));
  flow = insertNode(flow, { parent: group.id, path: 1, index: 0 }, agent('C'));
  assert.deepEqual(validate(flow, agents), []);
  assert.deepEqual(whereAfter(flow, 'B'), { parent: group.id, path: 0, index: 1 });
  flow = resizeLanes(flow, group.id, 3);
  assert.equal(findNode(flow, group.id)!.lanes!.length, 3);
  flow = resizeLanes(flow, group.id, 1);
  assert.equal(findNode(flow, group.id)!.lanes!.length, 2, 'never fewer than two');
  assert.equal(stepCount(flow), 3, 'the group is not a step');
  const steps = compile(flow);
  assert.deepEqual(steps.map((s) => s.name), ['a', 'b', 'c']);
});

// ── Approvals with an Approved and a Rejected path ────────────

const person = (title: string): FlowNode => step(title, 'human_gate', { config: { max_wait_minutes: 1440 } });
const approval = (title: string, yes: FlowNode[], no: FlowNode[]): FlowNode => {
  const n = person(title);
  n.step.config = { ...n.step.config, on_reject: 'branch' };
  return { ...n, yes, no };
};

test('an approval with paths writes an approved branch and routes a rejection to the other path', () => {
  const steps = compile([agent('Draft'), approval('Sign off', [agent('Send', 'writer')], [agent('Revise')])]);
  const gate = byName(steps, 'sign-off');
  assert.equal(gate.config?.on_reject, 'branch');
  assert.deepEqual(gate.branches, [{ condition: APPROVED_CONDITION, next_step: 'send' }]);
  assert.equal(gate.default_next, 'revise');
  assert.deepEqual(byName(steps, 'send').depends_on, ['sign-off']);
  assert.deepEqual(byName(steps, 'revise').depends_on, ['sign-off']);
});

test('an approval with paths is read back as the same flow, including empty and nested paths', () => {
  const shapes: FlowNode[][] = [
    [agent('Draft'), approval('Sign off', [agent('Send', 'writer')], [agent('Revise')])],
    [agent('Draft'), approval('Sign off', [agent('Send', 'writer')], [])],
    [agent('Draft'), approval('Sign off', [], [agent('Revise')]), agent('Log it')],
    [agent('Draft'), approval('First look', [approval('Second look', [agent('Send', 'writer')], [agent('Fix')])], [agent('Revise')])],
    [agent('Draft'), decide('Big?', [approval('Sign off', [agent('Send', 'writer')], [agent('Revise')])], [agent('Auto', 'writer')])],
  ];
  for (const flow of shapes) {
    const wire = compile(flow);
    const back = rewrap(wire);
    assert.ok(back.ok, back.ok ? '' : back.reason);
    if (back.ok) assert.ok(sameWire(compile(back.flow), wire));
  }
});

test('an empty Rejected path still gives the engine a step to route to, so the approved steps do not run', () => {
  const steps = compile([agent('Draft'), approval('Sign off', [agent('Send', 'writer')], [])]);
  const gate = byName(steps, 'sign-off');
  const carry = byName(steps, gate.default_next!);
  assert.equal(carry.config?.ui, CARRY_ON);
  assert.deepEqual(carry.depends_on, ['sign-off']);
});

test('switching paths on moves the following steps under Approved, and off puts them back', () => {
  const flow = [agent('Draft'), person('Sign off'), agent('Send', 'writer')];
  const on = setApprovalPaths(flow, 'Sign off', true);
  const gate = findNode(on, 'Sign off')!;
  assert.equal(on.length, 2, 'the step that followed moved inside');
  assert.deepEqual(gate.yes?.map((n) => n.id), ['Send']);
  assert.deepEqual(gate.no, []);
  assert.deepEqual(validate(on, agents), []);
  assert.ok(canRemovePaths(gate));
  const off = setApprovalPaths(on, 'Sign off', false);
  assert.deepEqual(off.map((n) => n.id), ['Draft', 'Sign off', 'Send']);
  assert.equal(findNode(off, 'Sign off')!.step.config?.on_reject, undefined);
  assert.ok(sameWire(compile(off), compile(flow)));
});

test('paths cannot be switched off while the Rejected path has steps', () => {
  const flow = [approval('Sign off', [], [agent('Revise')])];
  assert.equal(canRemovePaths(flow[0]), false);
  assert.equal(setApprovalPaths(flow, 'Sign off', false), flow);
});

test('steps left after an approval whose Rejected path is empty are flagged, because a rejection would run them', () => {
  const flow = [agent('Draft'), approval('Sign off', [agent('Send', 'writer')], []), agent('Log it')];
  const problems = validate(flow, agents);
  assert.equal(problems.length, 1);
  assert.match(problems[0].message, /after a rejection/);
  assert.deepEqual(validate([agent('Draft'), approval('Sign off', [agent('Send', 'writer')], [agent('Revise')]), agent('Log it')], agents), []);
});

test('an approval that stops the recipe on a no is left as it was', () => {
  const flow = [agent('Draft'), person('Sign off'), agent('Send', 'writer')];
  const steps = compile(flow);
  assert.equal(byName(steps, 'sign-off').config?.on_reject, undefined);
  assert.equal(byName(steps, 'sign-off').branches, undefined);
  assert.ok(rewrap(steps).ok);
});

test('a hand-written approval whose branches the builder would change is read-only', () => {
  const wire: WireStep[] = [
    { name: 'draft', kind: 'agent', agent_ref: 'triage', agent: 'triage' },
    { name: 'gate', kind: 'human_gate', depends_on: ['draft'], config: { on_reject: 'branch' }, branches: [{ condition: 'decision == "approved"', next_step: 'a' }], default_next: 'b' },
    { name: 'a', kind: 'agent', agent_ref: 'writer', agent: 'writer', depends_on: ['gate'] },
    { name: 'b', kind: 'agent', agent_ref: 'writer', agent: 'writer', depends_on: ['gate'] },
  ];
  const back = rewrap(wire);
  assert.equal(back.ok, false);
});
