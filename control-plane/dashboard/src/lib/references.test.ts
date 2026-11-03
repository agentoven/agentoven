/// <reference types="node" />
// Run with `npm test`. Pure helpers only: the options a field offers, the value that names nothing, and the
// server's "did you mean", which the shared cases in sdk/fixtures/refcheck-cases.json pin to the Go validator.
import assert from 'node:assert/strict';
import { existsSync, readFileSync } from 'node:fs';
import { test } from 'node:test';
import {
  agentOptions, channelOptions, configurationErrors, dedupe, describeProblem, filterOptions, groupOptions, isOpenProvider, modelOptions,
  nearest, problemsAt, problemsUnder, promptOptions, promptVersions, providerOptions, recipeOptions, rowOfPath, skillOptions, stepActive,
  toolOptions, unresolved, unresolvedMany, unresolvedModel, type RefOption,
} from './references.ts';

// The cases live in the community repository; the Pro dashboard finds them when the two are checked out side by side.
const FIXTURE_CANDIDATES = [
  new URL('../../../../sdk/fixtures/refcheck-cases.json', import.meta.url),
  new URL('../../../../agentoven/sdk/fixtures/refcheck-cases.json', import.meta.url),
];
const fixturePath = FIXTURE_CANDIDATES.find((u) => existsSync(u));

test('nearest agrees with every shared fixture case', { skip: fixturePath ? false : 'sdk/fixtures/refcheck-cases.json is not checked out next to this dashboard' }, () => {
  const cases = (JSON.parse(readFileSync(fixturePath!, 'utf8')) as { cases: { name: string; kind: string; value?: string; candidates?: string[]; expect?: string[] }[] }).cases
    .filter((c) => c.kind === 'nearest');
  assert.ok(cases.length >= 5, 'the fixture holds the nearest cases');
  for (const c of cases) assert.deepEqual(nearest(c.value ?? '', c.candidates ?? []), c.expect ?? [], c.name);
});

test('nearest ranks typos, ignores case and punctuation, and finds a part of a name', () => {
  assert.deepEqual(nearest('dokcer', ['docker', 'local', 'k8s']), ['docker']); // a swap is one edit
  assert.deepEqual(nearest('GPT4o', ['gpt-4o', 'gpt-4o-mini', 'llama3']), ['gpt-4o', 'gpt-4o-mini']);
  assert.deepEqual(nearest('haiku', ['claude-haiku-4-5', 'claude-sonnet-4-5']), ['claude-haiku-4-5']);
  assert.deepEqual(nearest('zzzzzz', ['gpt-4o', 'llama3']), []);
  assert.deepEqual(nearest('', ['a']), []);
  assert.deepEqual(nearest('---', ['a']), [], 'nothing but punctuation matches nothing');
  assert.equal(nearest('claude', ['claude-a', 'claude-b', 'claude-c', 'claude-d']).length, 3, 'at most three');
  assert.deepEqual(nearest('tool', ['tool', 'tool']), ['tool'], 'a repeated candidate is offered once');
});

test('an open provider is one of the three kinds, or one registered with no list', () => {
  assert.equal(isOpenProvider({ kind: 'openrouter', models: ['a'] }), true);
  assert.equal(isOpenProvider({ kind: 'litellm', models: ['a'] }), true);
  assert.equal(isOpenProvider({ kind: 'ollama', models: ['a'] }), true);
  assert.equal(isOpenProvider({ kind: 'anthropic', models: ['a'] }), false);
  assert.equal(isOpenProvider({ kind: 'anthropic', models: [] }), true);
  assert.equal(isOpenProvider({ kind: 'anthropic', models: null }), true);
});

test('options carry the state of what they stand for', () => {
  assert.deepEqual(providerOptions([{ name: 'a', kind: 'openai', last_test_healthy: false }, { name: 'b', kind: 'openai' }]).map((o) => o.state), ['unhealthy', undefined]);
  assert.deepEqual(modelOptions({ name: 'a', kind: 'openai', models: ['m1', 'm2'] }).map((o) => o.value), ['m1', 'm2']);
  assert.deepEqual(modelOptions(undefined), []);

  const tools = toolOptions([
    { name: 'search', enabled: true, capabilities: ['tool'] },
    { name: 'old', enabled: false, capabilities: ['tool'] },
    { name: 'mail', enabled: true, capabilities: ['notify'] },
  ], 'tool');
  assert.equal(tools[1].state, 'disabled');
  assert.equal(!!tools[1].disabled, false, 'a disabled tool can still be chosen: the server only warns for an optional one');
  assert.equal(tools[2].disabled, true, 'a tool without the capability cannot be picked for this use');
  assert.equal(toolOptions([{ name: 'mail', enabled: true, capabilities: ['notify'] }], 'notify')[0].disabled, false);

  assert.deepEqual(skillOptions([{ name: 'a', status: 'accepted' }, { name: 'b', status: 'needs_review' }, { name: 'c', status: 'pending' }]).map((o) => o.state), [undefined, 'unapproved', 'pending']);
  assert.deepEqual(channelOptions([{ name: 'live', kind: 'websocket', active: false }])[0], { value: 'live', label: 'live', detail: 'live feed', state: 'inactive', disabled: true });
});

test('agents: self is left out, a retired agent is shown and cannot be picked', () => {
  const opts = agentOptions([{ name: 'me', status: 'ready' }, { name: 'a', status: 'retired' }, { name: 'b', status: 'cooled' }, { name: 'c', status: 'burnt' }, { name: 'd', status: 'ready' }], 'me');
  assert.deepEqual(opts.map((o) => o.value), ['a', 'b', 'c', 'd']);
  assert.deepEqual(opts.map((o) => o.state), ['retired', 'cooled', 'burnt', undefined]);
  assert.deepEqual(opts.map((o) => !!o.disabled), [true, false, false, false]);
});

test('prompts: one option per name at its latest version, and the versions to choose from', () => {
  const opts = promptOptions([{ name: 'b', version: 1 }, { name: 'a', version: 1 }, { name: 'a', version: 3 }, { name: 'a', version: 2 }]);
  assert.deepEqual(opts.map((o) => [o.value, o.detail]), [['a', 'v3'], ['b', 'v1']]);
  assert.deepEqual(promptVersions(opts, 'a'), [3, 2, 1]);
  assert.deepEqual(promptVersions(opts, 'nope'), []);
  assert.deepEqual(promptVersions(undefined, 'a'), []);
});

test('recipes: an archived recipe and the one being edited are not offered', () => {
  assert.deepEqual(recipeOptions([{ name: 'a' }, { name: 'me' }, { name: 'old', archived_at: '2026-01-01' }], 'me').map((o) => o.value), ['a']);
});

test('a value that names nothing is reported with what it was probably meant to be', () => {
  const opts: RefOption[] = [{ value: 'summarize', label: 'summarize' }, { value: 'translate', label: 'translate' }, { value: 'gone', label: 'gone', state: 'retired', disabled: true }];
  assert.equal(unresolved('summarize', opts), null);
  assert.equal(unresolved('', opts), null, 'nothing chosen is the form\'s rule, not an unresolved value');
  assert.equal(unresolved('anything', undefined), null, 'a list that could not be read checks nothing');
  assert.deepEqual(unresolved('sumarize', opts), { value: 'sumarize', why: 'not found', suggestions: ['summarize'] });
  assert.equal(unresolved('qqqq', opts)?.suggestions.length, 0);
  assert.deepEqual(unresolved('gone', opts), { value: 'gone', why: 'retired', suggestions: [] });
  assert.deepEqual(unresolvedMany(['summarize', 'sumarize', 'zzz'], opts).map((u) => u.value), ['sumarize', 'zzz']);
  assert.deepEqual(unresolvedMany(undefined, opts), []);
});

test('a provider and model pair: a missing provider, a model its list lacks, and the open provider that allows a custom id', () => {
  const providers = [
    { name: 'claude', kind: 'anthropic', models: ['claude-haiku-4-5'] },
    { name: 'router', kind: 'openrouter', models: ['anthropic/claude-haiku-4.5'] },
    { name: 'bare', kind: 'openai', models: [] },
  ];
  assert.equal(unresolvedModel({ provider: '', model: 'x' }, providers), null, 'no provider chosen is a different message');
  assert.equal(unresolvedModel({ provider: 'claude', model: 'claude-haiku-4-5' }, providers), null);
  assert.equal(unresolvedModel({ provider: 'claude', model: '' }, providers), null);
  assert.equal(unresolvedModel({ provider: 'router', model: 'vendor/anything' }, providers), null, 'an open provider accepts a custom id');
  assert.equal(unresolvedModel({ provider: 'bare', model: 'whatever' }, providers), null, 'a provider with no list accepts any id');
  assert.equal(unresolvedModel({ provider: 'claude', model: 'x' }, undefined), null);
  assert.deepEqual(unresolvedModel({ provider: 'claud', model: 'x' }, providers), { field: 'provider', value: 'claud', why: 'not found', suggestions: ['claude'] });
  assert.deepEqual(unresolvedModel({ provider: 'claude', model: 'claude-hiku-4-5' }, providers), { field: 'model', value: 'claude-hiku-4-5', why: 'not found', suggestions: ['claude-haiku-4-5'] });
});

test('search matches every word, in the label, value, detail or group', () => {
  const opts: RefOption[] = [{ value: 'a', label: 'alpha', detail: 'first letter' }, { value: 'b', label: 'beta', group: 'greek' }];
  assert.deepEqual(filterOptions(opts, '').length, 2);
  assert.deepEqual(filterOptions(opts, 'FIRST let').map((o) => o.value), ['a']);
  assert.deepEqual(filterOptions(opts, 'greek').map((o) => o.value), ['b']);
  assert.deepEqual(filterOptions(opts, 'alpha greek'), []);
});

test('groups keep first-seen order with ungrouped options first; arrows skip rows that cannot be picked', () => {
  const opts: RefOption[] = [{ value: '1', label: '1', group: 'x' }, { value: '2', label: '2' }, { value: '3', label: '3', group: 'x' }, { value: '4', label: '4', group: 'y' }];
  assert.deepEqual(groupOptions(opts).map((g) => [g.group, g.options.map((o) => o.value)]), [['', ['2']], ['x', ['1', '3']], ['y', ['4']]]);

  const rows: RefOption[] = [{ value: 'a', label: 'a', disabled: true }, { value: 'b', label: 'b' }, { value: 'c', label: 'c', disabled: true }, { value: 'd', label: 'd' }];
  assert.equal(stepActive(rows, -1, 'first'), 1);
  assert.equal(stepActive(rows, 1, 1), 3, 'down skips the disabled row');
  assert.equal(stepActive(rows, 3, 1), 3, 'it stays at the end');
  assert.equal(stepActive(rows, 3, -1), 1);
  assert.equal(stepActive(rows, 1, -1), 1, 'and at the start');
  assert.equal(stepActive(rows, 0, 'last'), 3);
  assert.equal(stepActive([{ value: 'a', label: 'a', disabled: true }], -1, 'first'), -1);
  assert.equal(stepActive([], -1, 1), -1);
});

test('a list field holds each name once', () => {
  assert.deepEqual(dedupe(['a', 'b', 'a', 'c', 'b']), ['a', 'b', 'c']);
});

test('the server\'s refusal is read only when it is the invalid_configuration answer', () => {
  const body = {
    error: 'x', code: 'invalid_configuration',
    problems: [{ path: 'skills[0]', code: 'not_found', severity: 'error', kind: 'skill', value: 'sumarize', message: 'skills[0]: "sumarize" is not a skill in this kitchen', suggestions: ['summarize'] }, { nonsense: true }],
    warnings: [{ path: 'approval_tools[0]', code: 'not_usable', severity: 'warning', message: 'approval_tools[0]: tool "t" is disabled' }],
  };
  const got = configurationErrors(body);
  assert.equal(got?.problems.length, 1, 'an entry without a path and message is dropped');
  assert.equal(got?.warnings.length, 1);
  assert.equal(configurationErrors({ code: 'other' }), null);
  assert.equal(configurationErrors(null), null);
  assert.equal(configurationErrors('invalid_configuration'), null);
  assert.deepEqual(configurationErrors({ code: 'invalid_configuration' }), { problems: [], warnings: [] });
});

test('problems are found by the field they name, and sent paths map back to the row that made them', () => {
  const ps = [
    { path: 'ingredients[1].config.model', code: 'not_found', severity: 'error', message: 'm1' },
    { path: 'ingredients[1]', code: 'required', severity: 'error', message: 'm2' },
    { path: 'ingredients[10].name', code: 'duplicate', severity: 'error', message: 'm3' },
    { path: 'skills[0]', code: 'not_found', severity: 'error', message: 'm4' },
  ] as const;
  const list = [...ps];
  assert.deepEqual(problemsAt(list, 'skills[0]').map((p) => p.message), ['m4']);
  assert.deepEqual(problemsUnder(list, 'ingredients[1]').map((p) => p.message), ['m1', 'm2'], 'ingredients[1] is not ingredients[10]');
  assert.deepEqual(problemsAt(undefined, 'x'), []);
  // The form sent rows 0, 2 (row 1 had no name and was left out) and 3 as indexes 0, 1 and 2.
  const rows = [0, -1, 1, 2];
  assert.equal(rowOfPath('ingredients[1].name', 'ingredients', rows), 2);
  assert.equal(rowOfPath('ingredients[2]', 'ingredients', rows), 3);
  assert.equal(rowOfPath('skills[0]', 'ingredients', rows), -1);
  assert.equal(rowOfPath('ingredients[9]', 'ingredients', rows), -1);
});

test('a problem reads as the server wrote it, with suggestions added only when the message lacks them', () => {
  assert.equal(describeProblem({ path: 'a', code: 'not_found', severity: 'error', message: 'a: "x" is not a tool (did you mean "y"?)', suggestions: ['y'] }), 'a: "x" is not a tool (did you mean "y"?)');
  assert.equal(describeProblem({ path: 'a', code: 'not_found', severity: 'error', message: 'a: "x" is not a tool', suggestions: ['y', 'z'] }), 'a: "x" is not a tool (did you mean "y", "z"?)');
  assert.equal(describeProblem({ path: 'a', code: 'required', severity: 'error', message: 'a: needed' }), 'a: needed');
});
