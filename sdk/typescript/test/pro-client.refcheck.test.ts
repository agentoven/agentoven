import { afterAll, beforeAll, beforeEach, describe, expect, it } from 'vitest';
import { AgentOvenAPIError, ConfigurationError, ProClient } from '../src/pro-client.js';
import * as flow from '../src/flow.js';
import { Flow } from '../src/flow.js';
import { loadCatalog } from '../src/refcheck-catalog.js';
import { FakeControlPlane } from './fake-server.js';

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
  api.reset();
  pro = new ProClient({ url: api.url, apiKey: 'ao_key', kitchen: 'acme' });
});

const calls = () => api.requests.map((r) => `${r.method} ${r.path}`);
const writes = () => calls().filter((c) => !c.startsWith('GET '));

/** The kitchen's lists as the real routes answer them. */
function kitchen(over: Record<string, unknown> = {}): void {
  const lists: Record<string, unknown> = {
    '/api/v1/models/providers': [
      { name: 'anthropic', kind: 'anthropic', models: ['claude-haiku-4-5', 'claude-sonnet-4-5'], is_default: true },
      { name: 'flaky', kind: 'openai', models: ['gpt-4o'], is_default: false, last_test_healthy: false },
    ],
    '/api/v1/prompts': [{ name: 'triage', version: 1 }, { name: 'support', version: 2 }, { name: 'support', version: 3 }],
    '/api/v1/tools': [{ id: 't', name: 'search', enabled: true, capabilities: ['tool'] }],
    '/api/v1/skills': { skills: [{ name: 'pdf-reader', status: 'accepted' }, { name: 'shady', status: 'pending' }] },
    '/api/v1/agents?include_retired=true': [{ name: 'writer', status: 'ready' }, { name: 'old', status: 'retired' }],
    '/api/v1/recipes': [{ name: 'nightly' }],
    '/api/v1/channels': [{ name: 'ops' }],
    '/api/v1/environments': [{ id: 'e1', name: 'Production', slug: 'prod' }],
    '/api/v1/credentials': [{ name: 'partner-key' }],
    ...over,
  };
  for (const [path, body] of Object.entries(lists)) api.reply('GET', path, body);
}

describe('loadCatalog', () => {
  it('builds the catalog from the kitchen lists, and leaves out a list the edition does not have', async () => {
    kitchen(); // /api/v1/scenarios is not replied to: 404, the community edition
    const c = await pro.loadCatalog();
    expect(c.providers).toEqual([
      { name: 'anthropic', kind: 'anthropic', models: ['claude-haiku-4-5', 'claude-sonnet-4-5'] },
      { name: 'flaky', kind: 'openai', models: ['gpt-4o'], healthy: false },
    ]);
    expect(c.prompts).toEqual({ triage: 1, support: 3 });
    expect(c.tools).toEqual([{ name: 'search', enabled: true, capabilities: ['tool'] }]);
    expect(c.skills).toEqual([{ name: 'pdf-reader', status: 'accepted' }, { name: 'shady', status: 'pending' }]);
    expect(c.agents).toEqual([{ name: 'writer', status: 'ready' }, { name: 'old', status: 'retired' }]);
    expect(c.recipes).toEqual(['nightly']);
    expect(c.channels).toEqual(['ops']);
    expect(c.environments).toEqual(['prod']);
    expect(c.credentials).toEqual(['partner-key']);
    expect(c.scenarios).toBeUndefined();
    expect(c.roles).toBeUndefined();
    expect('scenarios' in c).toBe(false);
  });

  it('knows the scenarios when the edition lists them', async () => {
    kitchen({ '/api/v1/scenarios': [{ id: 'refund-world', version: 1 }] });
    expect((await pro.loadCatalog()).scenarios).toEqual({ known: true, names: ['refund-world'] });
  });

  it('a list the caller may not read (403) is unknown, never empty', async () => {
    kitchen();
    api.reply('GET', '/api/v1/credentials', { error: 'forbidden' }, 403);
    api.reply('GET', '/api/v1/tools', { error: 'forbidden' }, 403);
    const c = await pro.loadCatalog();
    expect(c.credentials).toBeUndefined();
    expect(c.tools).toBeUndefined();
    // so a reference into them is not refused
    const out = await pro.validateAgent({ approval_tools: ['whatever'], model_provider: 'anthropic', model_name: 'claude-haiku-4-5' });
    expect(out).toEqual({ valid: true, problems: [], warnings: [] });
  });

  it('a kitchen that cannot be read rejects: that is not "the kitchen has nothing"', async () => {
    kitchen();
    api.reply('GET', '/api/v1/prompts', { error: 'boom' }, 500);
    await expect(pro.loadCatalog()).rejects.toMatchObject({ statusCode: 500 });
    api.reply('GET', '/api/v1/prompts', { error: 'no key' }, 401);
    await expect(pro.validateAgent({ name: 'a' })).rejects.toBeInstanceOf(AgentOvenAPIError);
  });

  it('is a function of anything with the list calls', async () => {
    const empty = async () => [];
    const c = await loadCatalog({
      listProviders: empty, listPrompts: empty, listTools: empty, listSkills: empty, listAgents: empty, listRecipes: empty,
      listChannels: empty, listEnvironments: empty, listCredentials: empty,
      listScenarios: async () => { throw Object.assign(new Error('no'), { statusCode: 404 }); },
    });
    expect(c.providers).toEqual([]);
    expect(c.scenarios).toBeUndefined();
  });
});

describe('ConfigurationError', () => {
  const body = {
    error: 'the agent\'s configuration is not valid: summary_model: "gpt-4" is not a model of provider "openai" (did you mean "gpt-4o"?) (and 1 more)',
    code: 'invalid_configuration',
    problems: [
      { path: 'summary_model', code: 'not_found', severity: 'error', kind: 'model', value: 'gpt-4', within: 'openai', message: 'summary_model: "gpt-4" is not a model of provider "openai" (did you mean "gpt-4o"?)', suggestions: ['gpt-4o'] },
      { path: 'skills[0]', code: 'not_found', severity: 'error', kind: 'skill', value: 'pdf-redaer', message: 'skills[0]: skill "pdf-redaer" is not a skill in this kitchen (did you mean "pdf-reader"?)', suggestions: ['pdf-reader'] },
    ],
    warnings: [{ path: 'model_provider', code: 'not_usable', severity: 'warning', kind: 'provider', value: 'flaky', message: 'model_provider: provider "flaky" failed its last connectivity test' }],
  };

  it('a 422 invalid_configuration becomes one, with the problems and the warnings', async () => {
    api.reply('POST', '/api/v1/agents', body, 422);
    const err = await pro.createAgent({ name: 'a' }).catch((e) => e);
    expect(err).toBeInstanceOf(ConfigurationError);
    expect(err).toBeInstanceOf(AgentOvenAPIError);
    expect(err.statusCode).toBe(422);
    expect(err.problems.map((p: { path: string }) => p.path)).toEqual(['summary_model', 'skills[0]']);
    expect(err.problems[0].suggestions).toEqual(['gpt-4o']);
    expect(err.warnings).toHaveLength(1);
    expect(String(err)).toContain('did you mean "gpt-4o"?');
    expect(err.message).toContain('  - skills[0]: skill "pdf-redaer"');
    expect(err.message).toContain('warning: model_provider');
    expect(err.reason).toBe(body.error);
  });

  it('a 422 that is something else stays a plain API error', async () => {
    api.reply('POST', '/api/v1/agents', { error: 'unprocessable' }, 422);
    const err = await pro.createAgent({ name: 'a' }).catch((e) => e);
    expect(err).toBeInstanceOf(AgentOvenAPIError);
    expect(err).not.toBeInstanceOf(ConfigurationError);
  });

  it('survives a body without the lists', async () => {
    api.reply('POST', '/api/v1/agents', { error: 'x', code: 'invalid_configuration' }, 422);
    const err = (await pro.createAgent({ name: 'a' }).catch((e) => e)) as ConfigurationError;
    expect(err.problems).toEqual([]);
    expect(err.warnings).toEqual([]);
  });
});

describe('agentValidate and recipeValidate', () => {
  it('read what the server says about a stored agent and recipe', async () => {
    const report = { agent: 'a b', valid: false, problems: [{ path: 'skills[0]', code: 'not_found', severity: 'error', message: 'x' }], warnings: [] };
    api.reply('GET', '/api/v1/agents/a%20b/validate', report);
    api.reply('GET', '/api/v1/recipes/nightly/validate', { recipe: 'nightly', valid: true, problems: [], warnings: [] });
    expect(await pro.agentValidate('a b')).toEqual(report);
    expect((await pro.recipeValidate('nightly')).valid).toBe(true);
    expect(calls()).toEqual(['GET /api/v1/agents/a%20b/validate', 'GET /api/v1/recipes/nightly/validate']);
    expect(api.last.headers['x-kitchen']).toBe('acme');
  });
});

describe('validate: true on an agent', () => {
  const typo = { name: 'support', model_provider: 'anthropic', model_name: 'claude-haiku-4-5', summary_provider: 'anthropic', summary_model: 'claude-hiku-4-5' };

  it('createAgent refuses a typo before sending anything, with the nearest model', async () => {
    kitchen();
    const err = (await pro.createAgent(typo, { validate: true }).catch((e) => e)) as ConfigurationError;
    expect(err).toBeInstanceOf(ConfigurationError);
    expect(err.problems.map((p) => [p.path, p.code, p.value, p.within, p.suggestions])).toEqual([
      ['summary_model', 'not_found', 'claude-hiku-4-5', 'anthropic', ['claude-haiku-4-5']],
    ]);
    expect(err.statusCode).toBe(422);
    expect(writes()).toEqual([]);
  });

  it('createAgent sends a valid agent, carrying summary_provider, and ignores warnings', async () => {
    kitchen();
    api.reply('POST', '/api/v1/agents', { name: 'support', status: 'draft' });
    const ok = { ...typo, summary_model: 'claude-haiku-4-5', model_provider: 'flaky', model_name: 'gpt-4o' }; // flaky: a warning only
    expect((await pro.createAgent(ok, { validate: true })).name).toBe('support');
    expect(api.last.body).toEqual(ok);
    expect(api.last.body.summary_provider).toBe('anthropic');
  });

  it('is off by default and with validate: false: the server stays the authority', async () => {
    api.reply('POST', '/api/v1/agents', { name: 'support' });
    await pro.createAgent(typo);
    await pro.createAgent(typo, { validate: false });
    expect(calls()).toEqual(['POST /api/v1/agents', 'POST /api/v1/agents']); // no list was read
    expect(api.requests.every((r) => r.body.summary_model === 'claude-hiku-4-5')).toBe(true);
  });

  it('updateAgent judges the agent with the edit, and only holds it to what the edit made worse', async () => {
    kitchen();
    // the stored agent already names a skill that does not exist
    api.reply('GET', '/api/v1/agents/support', { name: 'support', status: 'ready', model_provider: 'anthropic', model_name: 'claude-haiku-4-5', skills: ['ghost'] });
    api.reply('PUT', '/api/v1/agents/support', { name: 'support' });
    await pro.updateAgent('support', { description: 'edited' }, { validate: true }); // what was wrong stays wrong, the edit is allowed
    expect(writes()).toEqual(['PUT /api/v1/agents/support']);

    api.requests = [];
    const err = (await pro.updateAgent('support', { summary_provider: 'anthropic', summary_model: 'claude-sonet-4-5' }, { validate: true }).catch((e) => e)) as ConfigurationError;
    expect(err.problems.map((p) => [p.path, p.suggestions])).toEqual([['summary_model', ['claude-sonnet-4-5']]]);
    expect(writes()).toEqual([]);
  });

  it('updateAgent reads the draft of a baked agent as part of it', async () => {
    kitchen();
    api.reply('GET', '/api/v1/agents/support', { name: 'support', status: 'ready', model_provider: 'anthropic', model_name: 'claude-haiku-4-5', draft: { summary_model: 'nope', summary_provider: 'anthropic' } });
    api.reply('PUT', '/api/v1/agents/support', { name: 'support' });
    // clearing the summary model fixes the draft; the edit is judged with the draft applied
    await pro.updateAgent('support', { summary_model: '', summary_provider: '' }, { validate: true });
    expect(writes()).toEqual(['PUT /api/v1/agents/support']);
  });

  it('bakeAgent checks the agent as a bake would, strictly, draft applied, and sends version and environment', async () => {
    kitchen();
    api.reply('GET', '/api/v1/agents/support', { name: 'support', status: 'ready', mode: 'managed' }); // no model at all
    const err = (await pro.bakeAgent('support', { validate: true }).catch((e) => e)) as ConfigurationError;
    expect(err.problems.map((p) => [p.path, p.code])).toEqual([['model_provider', 'required']]);
    expect(err.message).toContain('cannot be baked');
    expect(writes()).toEqual([]);

    api.reply('GET', '/api/v1/agents/support', { name: 'support', status: 'ready', draft: { model_provider: 'anthropic', model_name: 'claude-haiku-4-5' } });
    api.reply('POST', '/api/v1/agents/support/bake', { name: 'support', status: 'ready' });
    await pro.bakeAgent('support', { validate: true, version: '1.2.0', environment: 'prod' });
    expect(api.last.body).toEqual({ version: '1.2.0', environment: 'prod' });
  });

  it('recookAgent checks like a bake', async () => {
    kitchen();
    api.reply('GET', '/api/v1/agents/support', { name: 'support', status: 'ready', mode: 'managed' });
    await expect(pro.recookAgent('support', { validate: true })).rejects.toBeInstanceOf(ConfigurationError);
    api.reply('POST', '/api/v1/agents/support/recook', { name: 'support' });
    await pro.recookAgent('support');
    expect(writes()).toEqual(['POST /api/v1/agents/support/recook']);
  });

  it('validateAgent returns the problems and the warnings without sending a write', async () => {
    kitchen();
    const out = await pro.validateAgent({ name: 'a', model_provider: 'flaky', model_name: 'gpt-4o', summary_model: 'gpt-4' });
    expect(out.valid).toBe(false);
    expect(out.problems.map((p) => p.path)).toEqual(['summary_model']);
    expect(out.warnings.map((p) => p.path)).toEqual(['model_provider']);
    expect(writes()).toEqual([]);
    expect((await pro.validateAgent({ name: 'a', mode: 'managed' })).valid).toBe(true); // a draft may be incomplete
    expect((await pro.validateAgent({ name: 'a', mode: 'managed' }, { bake: true })).valid).toBe(false);
  });

  it('checks against the kitchen asked for, not the client\'s own', async () => {
    kitchen();
    await pro.validateAgent({ name: 'a' }, { kitchen: 'other' });
    expect(api.requests.every((r) => r.headers['x-kitchen'] === 'other')).toBe(true);
  });
});

describe('validate: true on a recipe', () => {
  const recipe = { name: 'r', description: '', steps: [{ name: 'a', kind: 'agent', agent_ref: 'writter' }] };

  it('createRecipe refuses a step that names no agent before sending', async () => {
    kitchen();
    const err = (await pro.createRecipe(recipe, { validate: true }).catch((e) => e)) as ConfigurationError;
    expect(err).toBeInstanceOf(ConfigurationError);
    expect(err.problems.map((p) => [p.path, p.suggestions])).toEqual([['steps[0].agent_ref', ['writer']]]);
    expect(writes()).toEqual([]);
  });

  it('createRecipe takes a Flow, and is unchanged without validate', async () => {
    kitchen();
    api.reply('POST', '/api/v1/recipes', { name: 'f' });
    const f = new Flow('f', '', [flow.agent('Write', 'writter')]);
    await expect(pro.createRecipe(f, { validate: true })).rejects.toBeInstanceOf(ConfigurationError);
    expect(writes()).toEqual([]);
    await pro.createRecipe(f);
    expect(writes()).toEqual(['POST /api/v1/recipes']);
  });

  it('updateRecipe forgives what the stored recipe already had wrong', async () => {
    kitchen();
    const bad = { name: 'f', description: '', steps: [{ name: 'Write', kind: 'agent', agent_ref: 'writter' }] };
    api.reply('GET', '/api/v1/recipes/f', bad);
    api.reply('PUT', '/api/v1/recipes/f', bad);
    await pro.updateRecipe(new Flow('f', 'new words', [flow.agent('Write', 'writter')]), { validate: true });
    expect(writes()).toEqual(['PUT /api/v1/recipes/f']);
    api.requests = [];
    await expect(pro.updateRecipe(new Flow('f', '', [flow.agent('Write', 'writter'), flow.agent('Edit', 'edtor')]), { validate: true })).rejects.toBeInstanceOf(ConfigurationError);
    expect(writes()).toEqual([]);
  });

  it('runRecipe checks the stored recipe and the environment asked for, before starting a run', async () => {
    kitchen();
    api.reply('GET', '/api/v1/recipes/r', { name: 'r', steps: [{ name: 'a', kind: 'agent', agent_ref: 'writer' }] });
    api.reply('POST', '/api/v1/recipes/r/bake', { run_id: 'run1', status: 'running' });
    const err = (await pro.runRecipe('r', {}, { environment: 'prd', validate: true }).catch((e) => e)) as ConfigurationError;
    expect(err.problems.map((p) => [p.path, p.kind, p.suggestions])).toEqual([['environment', 'environment', ['prod']]]);
    expect(writes()).toEqual([]);
    const run = await pro.runRecipe('r', { x: 1 }, { environment: 'prod', validate: true });
    expect(run.run_id).toBe('run1');
    expect(api.last.body).toEqual({ input: { x: 1 }, environment: 'prod' });
    await pro.runRecipe('r', {}, { environment: 'prd' }); // not asked: the server decides
    expect(calls().at(-1)).toBe('POST /api/v1/recipes/r/bake');
  });

  it('bakeRecipe takes validate too, and a retired agent in a step refuses', async () => {
    kitchen();
    api.reply('GET', '/api/v1/recipes/r', { name: 'r', steps: [{ name: 'a', kind: 'agent', agent_ref: 'old' }] });
    const err = (await pro.bakeRecipe('r', {}, undefined, { validate: true }).catch((e) => e)) as ConfigurationError;
    expect(err.problems.map((p) => [p.path, p.code])).toEqual([['steps[0].agent_ref', 'not_usable']]);
    expect(writes()).toEqual([]);
  });

  it('validateRecipe returns the problems, the warnings and valid', async () => {
    kitchen();
    const out = await pro.validateRecipe(recipe);
    expect(out.valid).toBe(false);
    expect(out.problems).toHaveLength(1);
    expect((await pro.validateRecipe({ name: 'r', steps: [{ name: 'a', kind: 'agent', agent_ref: 'writer' }] })).valid).toBe(true);
  });
});

describe('the lists the catalog reads', () => {
  it('listSkills and listPrompts accept the shapes the server uses', async () => {
    api.reply('GET', '/api/v1/skills', { skills: null });
    api.reply('GET', '/api/v1/prompts', null);
    expect(await pro.listSkills()).toEqual([]);
    expect(await pro.listPrompts()).toEqual([]);
    api.reply('GET', '/api/v1/skills', [{ name: 's', status: 'accepted' }]);
    expect(await pro.listSkills()).toEqual([{ name: 's', status: 'accepted' }]);
  });

  it('listAgents leaves retired agents out unless asked', async () => {
    api.reply('GET', '/api/v1/agents', []);
    api.reply('GET', '/api/v1/agents?include_retired=true', [{ name: 'old' }]);
    expect(await pro.listAgents()).toEqual([]);
    expect(await pro.listAgents({ includeRetired: true })).toEqual([{ name: 'old' }]);
  });
});
