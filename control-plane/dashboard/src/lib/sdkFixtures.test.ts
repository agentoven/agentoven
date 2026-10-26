/// <reference types="node" />
// The SDKs (Python and TypeScript) build recipes with their own flow builders. This test pins them to the
// dashboard's: sdk/fixtures/recipe-flows.json holds each flow as a neutral `spec` and the `wire` steps it
// must produce, and every `wire` here is what compile() writes. The SDK tests require the same `wire`.
//
// To change the format on purpose, run `UPDATE_FIXTURES=1 npm test`, then review the diff and update the SDKs.
import assert from 'node:assert/strict';
import { readFileSync, writeFileSync } from 'node:fs';
import { test } from 'node:test';
import {
  compile, decompile, mentionsExpression, newId, sameWire, uniqueName, validate, type FlowNode, type WireStep,
} from './recipeFlow.ts';

const FIXTURES = new URL('../../../../sdk/fixtures/recipe-flows.json', import.meta.url);

interface Spec {
  type: 'agent' | 'check' | 'lookup' | 'decide' | 'ask' | 'together' | 'sub_recipe';
  title?: string; agent?: string; retries?: number; timeout_secs?: number; top_k?: number;
  mentions?: string[]; when?: string; yes?: Spec[]; no?: Spec[];
  within?: string; notify?: string; domain?: string; emails?: string[]; approved?: Spec[]; rejected?: Spec[]; if_nobody_answers?: string;
  lanes?: Spec[][];
  recipe?: string; input?: Record<string, string>; output?: Record<string, string>;
}
interface Flow { name: string; description: string; spec: Spec[]; wire: WireStep[] }
interface Invalid { name: string; spec: Spec[]; expect: string }
interface Fixtures { _about: string; flows: Flow[]; invalid: Invalid[] }

const WAIT: Record<string, number> = { '1 hour': 60, '4 hours': 240, '1 day': 1440, '3 days': 4320, 'no limit': 0 };

/** The neutral spec as a flow the dashboard builder would hold. Names are given in the order steps are read. */
function toFlow(specs: Spec[]): FlowNode[] {
  const taken = new Set<string>();
  const name = (title: string): string => { const n = uniqueName(title, taken); taken.add(n); return n; };
  const make = (t: Spec): FlowNode => {
    if (t.type === 'together') return { id: newId(), step: { name: '', kind: 'parallel' }, lanes: (t.lanes ?? []).map((lane) => lane.map(make)) };
    const title = t.title ?? '';
    const step: WireStep = { name: name(title), kind: 'agent', config: { title } };
    switch (t.type) {
      case 'agent': case 'check':
        step.kind = t.type === 'agent' ? 'agent' : 'evaluator';
        step.agent_ref = t.agent;
        if (t.retries) step.max_retries = t.retries;
        if (t.timeout_secs) step.timeout_secs = t.timeout_secs;
        return { id: newId(), step };
      case 'lookup':
        step.kind = 'rag';
        step.config = { title, top_k: t.top_k ?? 3 };
        return { id: newId(), step };
      case 'sub_recipe':
        step.kind = 'sub_recipe';
        step.recipe_ref = t.recipe;
        if (t.input) step.input_mapping = t.input;
        if (t.output) step.output_mapping = t.output;
        return { id: newId(), step };
      case 'decide': {
        step.kind = 'condition';
        step.config = { title, expression: t.when ?? (t.mentions ? mentionsExpression(t.mentions) : '') };
        return { id: newId(), step, yes: (t.yes ?? []).map(make), no: (t.no ?? []).map(make) };
      }
      case 'ask': {
        step.kind = 'human_gate';
        const paths = t.approved !== undefined || t.rejected !== undefined;
        step.config = {
          title, max_wait_minutes: WAIT[t.within ?? '1 day'],
          ...(paths ? { on_reject: 'branch' } : {}),
          ...(paths && t.if_nobody_answers === 'reject' ? { on_timeout: 'branch' } : {}),
        };
        if (t.notify) step.notify_tools = [t.notify];
        if (t.domain) step.approver_domain = t.domain;
        if (t.emails) step.approver_emails = t.emails;
        return paths ? { id: newId(), step, yes: (t.approved ?? []).map(make), no: (t.rejected ?? []).map(make) } : { id: newId(), step };
      }
    }
  };
  return specs.map(make);
}

const json = (v: unknown): WireStep[] => JSON.parse(JSON.stringify(v)) as WireStep[];

/** One line per spec and per step: readable, and a changed step shows as one changed line. */
function format(f: Fixtures): string {
  const flows = f.flows.map((fl) => [
    '    {',
    `      "name": ${JSON.stringify(fl.name)},`,
    `      "description": ${JSON.stringify(fl.description)},`,
    `      "spec": ${JSON.stringify(fl.spec)},`,
    `      "wire": [\n${fl.wire.map((s) => `        ${JSON.stringify(s)}`).join(',\n')}\n      ]`,
    '    }',
  ].join('\n')).join(',\n');
  const invalid = f.invalid.map((i) => `    ${JSON.stringify(i)}`).join(',\n');
  return `{\n  "_about": ${JSON.stringify(f._about)},\n  "flows": [\n${flows}\n  ],\n  "invalid": [\n${invalid}\n  ]\n}\n`;
}

const fixtures = JSON.parse(readFileSync(FIXTURES, 'utf8')) as Fixtures;

if (process.env.UPDATE_FIXTURES) {
  for (const fl of fixtures.flows) fl.wire = json(compile(toFlow(fl.spec)));
  writeFileSync(FIXTURES, format(fixtures));
  console.log('sdk/fixtures/recipe-flows.json rewritten from the dashboard builder');
}

test('every fixture is what the dashboard builder writes', () => {
  assert.ok(fixtures.flows.length > 0);
  for (const fl of fixtures.flows) {
    assert.deepEqual(json(compile(toFlow(fl.spec))), fl.wire, `${fl.name}: the SDK fixture is out of date; run UPDATE_FIXTURES=1 npm test and update the SDKs`);
  }
});

test('the dashboard can draw and write back every flow an SDK builds', () => {
  for (const fl of fixtures.flows) {
    const back = decompile(json(fl.wire));
    assert.ok(back.ok, `${fl.name}: ${back.ok ? '' : back.reason}`);
    if (back.ok) assert.ok(sameWire(compile(back.flow), fl.wire), `${fl.name} changes when the builder saves it`);
  }
});

test('the dashboard rejects the invalid flows for the reason the fixture gives', () => {
  for (const bad of fixtures.invalid) {
    const agents = new Set(JSON.stringify(bad.spec).match(/"agent":"([^"]+)"/g)?.map((m) => m.slice(9, -1)) ?? []);
    const problems = validate(toFlow(bad.spec), agents).map((p) => p.message);
    assert.ok(problems.some((m) => m.includes(bad.expect)), `${bad.name}: wanted "${bad.expect}", got ${JSON.stringify(problems)}`);
  }
});
