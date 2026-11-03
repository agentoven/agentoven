/// <reference types="node" />
// The dropdowns, rendered to HTML (see lib/componentHarness): what the open list shows, what a value that names
// nothing looks like, and when the custom model id way out is there. Run with `npm test`.
import assert from 'node:assert/strict';
import { before, test } from 'node:test';
import { agentUnresolved, buildIngredients, emptyRow, type IngredientRow, type RefCatalog } from '../lib/agentRefs.ts';
import { loadModule, render, h } from '../lib/componentHarness.ts';
import { agentOptions, skillOptions, toolOptions, type RefOption } from '../lib/references.ts';

interface Mod {
  ReferenceSelect: unknown;
  ReferenceMultiSelect: unknown;
  ModelReferenceSelect: unknown;
}
interface EditorMod { IngredientRowsEditor: unknown }
interface BadgeMod { ProblemList: unknown }

let sel: Mod;
let editor: EditorMod;
let list: BadgeMod;
before(async () => {
  sel = await loadModule<Mod>('components/ReferenceSelect.tsx');
  editor = await loadModule<EditorMod>('components/IngredientsEditor.tsx');
  list = await loadModule<BadgeMod>('components/ProblemList.tsx');
});

const noop = () => undefined;
const tools: RefOption[] = toolOptions([
  { name: 'search', enabled: true, capabilities: ['tool'] },
  { name: 'old-search', enabled: false, capabilities: ['tool'] },
  { name: 'mail', enabled: true, capabilities: ['notify'] },
]);

test('the open list shows exactly what exists, each with its state', () => {
  const html = render(h(sel.ReferenceSelect, { label: 'Tool', options: tools, value: '', onChange: noop, defaultOpen: true }));
  assert.equal((html.match(/role="option"/g) ?? []).length, 3);
  assert.match(html, /role="listbox"/);
  assert.match(html, /data-state="disabled"/, 'the disabled tool says so');
  assert.match(html, /no tool capability/, 'a notify-only tool says why it cannot be picked');
  assert.match(html, /aria-disabled="true"/);
  assert.doesNotMatch(html, /data-unresolved/);
});

test('a value that names nothing shows as an error chip with the nearest values, and the trigger is marked invalid', () => {
  const html = render(h(sel.ReferenceSelect, { label: 'Tool', options: tools, value: 'serch', onChange: noop }));
  assert.match(html, /data-unresolved="serch"/);
  assert.match(html, /not found: <span class="font-mono">serch<\/span>/);
  assert.match(html, /Did you mean/);
  assert.match(html, />search<\/button>/, 'the nearest value is offered as a one click fix');
  assert.match(html, /aria-invalid="true"/);
  assert.match(html, /Remove/);
});

test('a retired agent that is still named is an error, not a quiet choice', () => {
  const opts = agentOptions([{ name: 'gone', status: 'retired' }, { name: 'live', status: 'ready' }]);
  const html = render(h(sel.ReferenceSelect, { label: 'Agent', options: opts, value: 'gone', onChange: noop }));
  assert.match(html, /retired: <span class="font-mono">gone<\/span>/);
});

test('a list that could not be read checks nothing: the value is shown plainly', () => {
  const html = render(h(sel.ReferenceSelect, { label: 'Tool', options: undefined, value: 'anything', onChange: noop }));
  assert.doesNotMatch(html, /data-unresolved/);
  assert.match(html, /anything/);
  const open = render(h(sel.ReferenceSelect, { label: 'Tool', options: undefined, value: '', onChange: noop, defaultOpen: true }));
  assert.match(open, /cannot be listed here/);
});

test('with nothing registered the list says what to register and links to the page that does it', () => {
  const html = render(h(sel.ReferenceMultiSelect, { label: 'Skills', options: [], value: [], onChange: noop, defaultOpen: true, empty: { what: 'skills', to: '/skills', link: 'Register a skill' } }));
  assert.match(html, /There are no skills yet/);
  assert.match(html, /href="\/skills"[^>]*>Register a skill</);
  assert.equal((html.match(/role="option"/g) ?? []).length, 0);
});

test('a multi select holds each name once, offers what is not chosen yet, and flags a chosen name that is gone', () => {
  const skills = skillOptions([{ name: 'summarize', status: 'accepted' }, { name: 'translate', status: 'accepted' }, { name: 'draft', status: 'needs_review' }]);
  const html = render(h(sel.ReferenceMultiSelect, { label: 'Skills', options: skills, value: ['summarize', 'summarize', 'sumarize'], onChange: noop, defaultOpen: true }));
  const chosen = html.slice(0, html.indexOf('role="combobox"'));
  assert.equal((chosen.match(/<li /g) ?? []).length, 2, 'the repeated name is shown once');
  assert.match(chosen, /data-unresolved="sumarize"/);
  assert.match(chosen, /Did you mean/);
  const optionLabels = [...html.matchAll(/role="option"[^>]*>.*?<span class="block truncate font-mono text-\[13px\]">([^<]+)</g)].map((m) => m[1]);
  assert.deepEqual(optionLabels, ['translate', 'draft'], 'what is already chosen is not offered again');
  assert.match(html, /data-state="unapproved"/);
});

const providers = [
  { name: 'claude', kind: 'anthropic', models: ['claude-haiku-4-5', 'claude-sonnet-4-5'] },
  { name: 'router', kind: 'openrouter', models: ['anthropic/claude-haiku-4.5'] },
  { name: 'bare', kind: 'openai', models: [] },
];
const model = (props: Record<string, unknown>) => render(h(sel.ModelReferenceSelect, { label: 'Model', providers, onChange: noop, provider: '', model: '', ...props }));

test('the model list does not exist until a provider is chosen', () => {
  const html = model({});
  assert.match(html, /aria-label="Model: provider"/);
  assert.doesNotMatch(html, /aria-label="Model: model"/);
  assert.doesNotMatch(html, /custom model id/i);
});

test('once a provider is chosen the model list holds that provider\'s models only', () => {
  const html = model({ provider: 'claude', defaultOpen: false });
  assert.match(html, /aria-label="Model: model"/);
  const open = render(h(sel.ReferenceSelect, { label: 'Model: model', options: providers[0].models.map((m) => ({ value: m, label: m })), value: '', onChange: noop, defaultOpen: true }));
  assert.equal((open.match(/role="option"/g) ?? []).length, 2);
});

test('the custom model id way out appears only for an open provider that has been chosen', () => {
  assert.doesNotMatch(model({ provider: 'claude', model: 'claude-haiku-4-5' }), /Enter a custom model id/, 'anthropic lists the whole truth');
  assert.match(model({ provider: 'router', model: 'anthropic/claude-haiku-4.5' }), /Enter a custom model id/, 'openrouter serves ids its list does not name');
  assert.match(model({ provider: 'bare' }), /aria-label="Custom model id"/, 'a provider with no list takes a typed id straight away');
  assert.doesNotMatch(model({}), /custom model id/i, 'not before a provider is chosen');
});

test('a custom id on an open provider is accepted, with a note; the same id on a closed provider is an error chip', () => {
  const open = model({ provider: 'router', model: 'vendor/not-listed' });
  assert.doesNotMatch(open, /data-unresolved/);
  assert.match(open, /aria-label="Custom model id"/, 'it opens in the custom id box so the person sees what was typed');
  assert.match(open, /serves models its list does not name/);

  const closed = model({ provider: 'claude', model: 'claude-hiku-4-5' });
  assert.match(closed, /data-unresolved="claude-hiku-4-5"/);
  assert.match(closed, />claude-haiku-4-5<\/button>/, 'and offers the nearest model');
});

test('a provider that does not exist is an error chip on the provider, with the nearest provider', () => {
  const html = model({ provider: 'claud', model: 'x' });
  assert.match(html, /data-unresolved="claud"/);
  assert.match(html, />claude<\/button>/);
});

test('a model with no provider: called out for the primary model; for the summarizer it is looked up among every provider', () => {
  const required = model({ provider: '', model: 'gpt-4o-mini' });
  assert.match(required, /has no provider/);
  const optional = model({ provider: '', model: 'claude-haiku-4-5', providerOptional: true });
  assert.match(optional, /found among every provider/);
  assert.doesNotMatch(optional, /data-unresolved/);
  const gone = model({ provider: '', model: 'claude-hiku-4-5', providerOptional: true, providers: providers.slice(0, 1) });
  assert.match(gone, /data-unresolved="claude-hiku-4-5"/);
  assert.match(gone, />claude-haiku-4-5<\/button>/);
});

test('server problems for a field are shown under it', () => {
  const html = model({ provider: 'claude', model: 'claude-haiku-4-5', problems: [{ path: 'summary_model', code: 'unlisted', severity: 'warning', message: 'summary_model: x is not in the list' }] });
  assert.match(html, /summary_model: x is not in the list/);
  assert.match(html, /text-amber-300/, 'a warning is calmer than an error');
});

// ── an agent's ingredients ───────────────────────────────────

const catalog: RefCatalog = {
  providers,
  tools,
  skills: skillOptions([{ name: 'summarize', status: 'accepted' }]),
  agents: agentOptions([{ name: 'helper', status: 'ready' }]),
  prompts: [{ value: 'support', label: 'support', detail: 'v3' }],
};
const row = (kind: IngredientRow['kind'], name: string, config: Record<string, unknown> = {}): IngredientRow => ({ ...emptyRow(kind), name, configJson: JSON.stringify(config) });
const editorHtml = (rows: IngredientRow[], extra: Record<string, unknown> = {}) => render(h(editor.IngredientRowsEditor, { rows, catalog, onChange: noop, ...extra }));

test('an ingredient row asks for the thing its kind uses from what the kitchen has', () => {
  const html = editorHtml([row('tool', 'search'), row('prompt', 'support', { version: 2 }), row('skill', 'summarize'), row('model', '', { provider: 'claude', model: 'claude-haiku-4-5' }), row('vectorstore', '', { backend: 'pgvector', index: 'docs' })]);
  for (const aria of ['Tool of ingredient', 'Prompt of ingredient', 'Skill of ingredient', 'Model of ingredient: provider', 'Vector store backend of ingredient']) assert.match(html, new RegExp(`aria-label="${aria}"`), aria);
  assert.match(html, /aria-label="Prompt version"/);
  assert.match(html, /<option value="3">Version 3<\/option>/, 'the versions the prompt really has');
  assert.doesNotMatch(html, /data-unresolved/);
});

test('an ingredient that names a tool, prompt, skill or provider that is gone shows the error chip on its row', () => {
  const html = editorHtml([row('tool', 'serch'), row('prompt', 'suport'), row('skill', 'sumarize'), row('embedding', '', { provider: 'claud' })]);
  for (const v of ['serch', 'suport', 'sumarize', 'claud']) assert.match(html, new RegExp(`data-unresolved="${v}"`), v);
  assert.equal((html.match(/border-red-500\/50/g) ?? []).length >= 4, true, 'each of those rows is marked');
});

test('a retriever points only at the embedding and vector store of the same agent', () => {
  const rows = [row('embedding', 'emb', { provider: 'claude' }), row('vectorstore', 'vs', { backend: 'pgvector', index: 'docs' }), row('retriever', 'r', { embedding_ref: 'nope', vectorstore_ref: 'docs' })];
  const html = editorHtml(rows);
  assert.match(html, /data-unresolved="nope"/);
  assert.doesNotMatch(html, /data-unresolved="docs"/);
});

test('settings that are not JSON are called out on the row instead of being saved as {}', () => {
  const html = editorHtml([{ ...emptyRow('tool'), configJson: '{oops' }]);
  assert.match(html, /not a JSON object/);
});

test('the server\'s problems for a row are listed on it, the ones a dropdown already shows are not repeated', () => {
  const html = editorHtml([row('tool', 'search')], { problemsByRow: { 0: [
    { path: 'ingredients[0].name', code: 'not_usable', severity: 'warning', message: 'ingredients[0].name: tool search is disabled' },
    { path: 'ingredients[0].config.timeout', code: 'wrong_type', severity: 'error', message: 'ingredients[0].config.timeout: must be a number' },
  ] } });
  assert.match(html, /tool search is disabled/, 'under the tool dropdown');
  assert.match(html, /must be a number/, 'under the row');
});

test('the problem list names the field, the sentence and the nearest values, and sets warnings apart', () => {
  const html = render(h(list.ProblemList, {
    problems: [{ path: 'skills[0]', code: 'not_found', severity: 'error', kind: 'skill', value: 'sumarize', message: 'skills[0]: "sumarize" is not a skill in this kitchen', suggestions: ['summarize'] }],
    warnings: [{ path: 'approval_tools[0]', code: 'not_usable', severity: 'warning', message: 'approval_tools[0]: tool "t" is disabled' }],
  }));
  assert.match(html, /<code[^>]*>skills\[0\]<\/code>/);
  assert.match(html, /is not a skill in this kitchen/);
  assert.match(html, /Did you mean <span><code class="font-mono">summarize<\/code><\/span>/);
  assert.match(html, /data-severity="warning"/);
  assert.match(html, /data-severity="error"/);
  assert.equal(render(h(list.ProblemList, {})), '');
});

// ── a form is not saved while a reference names nothing ──────

const empty = { provider: '', model: '' };
const refs = (over: Partial<Parameters<typeof agentUnresolved>[0]> = {}) => ({ model: empty, backup: empty, summary: empty, skills: [], approvalTools: [], subagents: [], ingredients: [], ...over });

test('a form with nothing unresolved can be saved', () => {
  assert.deepEqual(agentUnresolved(refs({ model: { provider: 'claude', model: 'claude-haiku-4-5' }, skills: ['summarize'], subagents: ['helper'], approvalTools: ['search'], ingredients: [row('tool', 'search')] }), catalog), []);
});

test('each kind of reference blocks the save until it is replaced or removed', () => {
  const blocked = agentUnresolved(refs({
    model: { provider: 'claud', model: 'x' },
    backup: { provider: 'claude', model: 'claude-hiku-4-5' },
    summary: { provider: 'router', model: 'vendor/custom' },
    skills: ['summarize', 'sumarize'],
    approvalTools: ['serch'],
    subagents: ['helpr'],
    ingredients: [row('prompt', 'suport'), { ...emptyRow('tool'), configJson: '{oops' }],
  }), catalog);
  assert.deepEqual(blocked.map((b) => b.where), [
    'Model provider', 'Backup model', 'Skills', 'Approval tools', 'Subagents', 'Ingredient 1 (suport) prompt', 'Ingredient 2 (tool)',
  ]);
  assert.deepEqual(blocked[2].suggestions, ['summarize']);
  assert.equal(blocked.some((b) => b.where === 'Summary model'), false, 'a custom id on an open provider is not an error');
});

test('removing the value clears the block, and a list that could not be read blocks nothing', () => {
  assert.equal(agentUnresolved(refs({ skills: ['sumarize'] }), catalog).length, 1);
  assert.equal(agentUnresolved(refs({ skills: [] }), catalog).length, 0);
  assert.equal(agentUnresolved(refs({ skills: ['x'], subagents: ['y'], model: { provider: 'z', model: 'w' } }), {}).length, 0);
});

test('the ingredients sent keep their rows: a problem at ingredients[2] maps back, and a blank row is left out', () => {
  const rows = [row('tool', 'search'), emptyRow('tool'), row('skill', 'summarize')];
  const built = buildIngredients(rows, 1);
  assert.deepEqual(built.sentAt, [1, -1, 2], 'the system prompt is index 0');
  assert.deepEqual(built.ingredients.map((i) => [i.kind, i.name]), [['tool', 'search'], ['skill', 'summarize']]);
});
