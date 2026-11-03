import { readFileSync } from 'node:fs';
import { describe, expect, it } from 'vitest';
import { applyEdit, checkAgent, checkRecipe, introduced, nearest, split, withDraft, type AgentConfig, type Catalog, type Problem, type RecipeConfig } from '../src/refcheck.js';

/**
 * The cases every implementation of the reference checks runs (sdk/fixtures/refcheck-cases.json): the Go validator
 * the server uses, the Python SDK, this one and the Rust client. A problem is identified by (path, code, severity,
 * kind, value, within), compared as a sorted multiset; suggestions are compared where a case lists them.
 */
const FIXTURE = new URL('../../fixtures/refcheck-cases.json', import.meta.url);

interface Case {
  name: string;
  kind: 'agent' | 'recipe' | 'nearest';
  catalog?: string;
  options?: { bake?: boolean; environment?: string };
  input?: AgentConfig & RecipeConfig;
  value?: string;
  candidates?: string[];
  expect: Problem[] | string[];
}

const fixture = JSON.parse(readFileSync(FIXTURE, 'utf8')) as { catalogs: Record<string, Catalog>; cases: Case[] };

const key = (p: Problem): string => [p.path, p.code, p.severity, p.kind ?? '', p.value ?? '', p.within ?? ''].join('|');
const keys = (ps: Problem[]): string[] => ps.map(key).sort();

describe('refcheck fixture cases', () => {
  it('the fixture is the one the suites agree on', () => {
    // A loader that read nothing would pass every case: hold it to the size of what it must run.
    expect(fixture.cases.length).toBe(71);
    expect(Object.keys(fixture.catalogs).sort()).toEqual(['base', 'closed', 'empty', 'pro']);
    expect(new Set(fixture.cases.map((c) => c.kind))).toEqual(new Set(['agent', 'recipe', 'nearest']));
  });

  for (const tc of fixture.cases) {
    it(tc.name, () => {
      if (tc.kind === 'nearest') {
        expect(nearest(tc.value!, tc.candidates!, 3)).toEqual(tc.expect);
        return;
      }
      const catalog = fixture.catalogs[tc.catalog!];
      const opts = { bake: tc.options?.bake ?? false, environment: tc.options?.environment ?? '' };
      const got = tc.kind === 'agent' ? checkAgent(tc.input!, catalog, opts) : checkRecipe(tc.input!, catalog, opts);
      const want = tc.expect as Problem[];
      expect(keys(got), got.map((p) => p.message).join('\n')).toEqual(keys(want));
      const byKey = new Map(got.map((p) => [key(p), p]));
      for (const w of want) {
        if (w.suggestions !== undefined) expect(byKey.get(key(w))?.suggestions ?? [], `suggestions for ${w.path}`).toEqual(w.suggestions);
      }
      for (const p of got) expect(p.message, `message of ${p.path}`).not.toBe('');
    });
  }
});

describe('refcheck beyond the fixture', () => {
  const base = fixture.catalogs.base;

  it('a part of the catalog that is left out is not checked; an empty one refuses', () => {
    const agent: AgentConfig = { name: 'a', model_provider: 'nope', model_name: 'x', skills: ['ghost'], subagents: ['ghost'] };
    expect(checkAgent(agent, {}).filter((p) => p.severity === 'error')).toEqual([]);
    const refused = checkAgent(agent, fixture.catalogs.empty).map((p) => p.kind);
    expect(refused).toEqual(expect.arrayContaining(['provider', 'skill', 'agent']));
  });

  it('a recipe is not refused for lists the caller could not read', () => {
    const recipe: RecipeConfig = {
      name: 'r',
      default_environment: 'nowhere',
      steps: [{ name: 'a', kind: 'agent', agent_ref: 'ghost', auth_key_ref: 'ghost', notify_tools: ['ghost'] }, { name: 'n', kind: 'notify', config: { channels: ['ghost'] } }],
    };
    expect(checkRecipe(recipe, {})).toEqual([]);
  });

  it('a configuration is read as it comes: wrong-typed fields and hostile names do not throw', () => {
    const agent = { name: 5, skills: 'pdf-reader', ingredients: [null, 7, { kind: 'tool', name: 'constructor' }], budget: 'x', bot: [], guardrails: 3 } as unknown as AgentConfig;
    expect(() => checkAgent(agent, base)).not.toThrow();
    const prompt: AgentConfig = { ingredients: [{ kind: 'prompt', name: 'constructor' }, { kind: 'prompt', name: '__proto__', config: { version: 1 } }] };
    expect(checkAgent(prompt, base).map((p) => `${p.path}:${p.code}`)).toEqual(['ingredients[0].name:not_found', 'ingredients[1].kind:duplicate', 'ingredients[1].name:not_found']);
  });

  it('a name is limited in bytes, as the server counts it', () => {
    const wide = 'é'.repeat(65); // 65 characters, 130 bytes
    expect(checkAgent({ name: wide }, base).map((p) => p.path)).toEqual(['name']);
    expect(checkAgent({ name: 'é'.repeat(64) }, base)).toEqual([]);
  });

  it('introduced keeps what an edit made worse and forgives what was already wrong', () => {
    const before = checkAgent({ skills: ['ghost'] }, base);
    const after = checkAgent({ skills: ['ghost', 'phantom'] }, base);
    expect(introduced(before, after).map((p) => p.value)).toEqual(['phantom']);
    expect(introduced(before, before)).toEqual([]);
    // a warning is never held against a write
    expect(introduced([], checkAgent({ skills: ['shady'] }, base))).toEqual([]);
    expect(split(checkAgent({ skills: ['shady'] }, base)).warnings).toHaveLength(1);
  });

  it('applyEdit follows the server: empty means unchanged, but "" clears the summary model and [] clears lists', () => {
    const agent: AgentConfig = {
      model_provider: 'anthropic', model_name: 'claude-haiku-4-5', summary_provider: 'openai', summary_model: 'gpt-4o', skills: ['pdf-reader'],
      subagents: ['writer'], ingredients: [{ kind: 'tool', name: 'search' }],
    };
    const edited = applyEdit(agent, { model_name: '', summary_model: '', summary_provider: '', subagents: [], skills: [], ingredients: [] });
    expect(edited).toMatchObject({ model_name: 'claude-haiku-4-5', summary_model: '', summary_provider: '', subagents: [], skills: [] });
    expect(edited.ingredients).toHaveLength(1);
    expect(applyEdit(agent, { summary_model: undefined }).summary_model).toBe('gpt-4o');
    expect(agent.summary_model).toBe('gpt-4o'); // the original is left alone
  });

  it('withDraft lays the draft over a baked agent and drops it', () => {
    const baked = { name: 'a', model_provider: 'anthropic', model_name: 'claude-haiku-4-5', draft: { model_name: 'claude-sonnet-4-5' } } as AgentConfig;
    expect(withDraft(baked)).toEqual({ name: 'a', model_provider: 'anthropic', model_name: 'claude-sonnet-4-5' });
    expect(withDraft({ name: 'a' })).toEqual({ name: 'a' });
  });
});
