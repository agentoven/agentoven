/**
 * Build the `Catalog` the reference checks read (`refcheck.ts`) from the kitchen's own lists, through a client.
 *
 * A list the caller may not read (HTTP 403, or 404 for a route this edition does not have: scenarios are Pro's) leaves
 * that part of the catalog out, so what it would have checked is left unchecked, never judged against an empty list.
 * Any other failure (the server down, a 500, a 401) rejects: the kitchen could not be read, which is not the same as
 * "the kitchen has nothing". Roles have no list route, so a recipe's `approver_roles` are left to the server.
 */
import type { Catalog } from './refcheck.js';
import type { Environment, NotificationChannel, Provider, Recipe, Scenario, Tool } from './types.js';

/** A skill as the skills list returns it. */
export interface SkillSummary {
  name: string;
  status: string;
}

/** A prompt as the prompts list returns it (`version` is the version listed). */
export interface PromptSummary {
  name: string;
  version: number;
}

/** An agent as the agents list returns it. */
export interface AgentSummary {
  name: string;
  status?: string;
}

/** The list calls the catalog is read through; `ProClient` has them all. */
export interface CatalogSource {
  listProviders(): Promise<Provider[]>;
  listPrompts(): Promise<PromptSummary[]>;
  listTools(): Promise<Tool[]>;
  listSkills(): Promise<SkillSummary[]>;
  listAgents(opts?: { includeRetired?: boolean }): Promise<AgentSummary[]>;
  listRecipes(): Promise<Recipe[]>;
  listChannels(): Promise<NotificationChannel[]>;
  listEnvironments(): Promise<Environment[]>;
  listCredentials(): Promise<Record<string, unknown>[]>;
  listScenarios(): Promise<Scenario[]>;
}

/** The result of one list, or undefined when the caller may not read it (403) or the edition has no such list (404). */
async function readable<T>(read: () => Promise<T>): Promise<T | undefined> {
  try {
    return await read();
  } catch (err) {
    const status = (err as { statusCode?: unknown } | null)?.statusCode;
    if (status === 403 || status === 404) return undefined;
    throw err;
  }
}

const names = (items: { name?: unknown }[]): string[] => items.map((i) => String(i.name ?? '')).filter((n) => n !== '');

/** Read what the kitchen of `client` has. Rejects when the kitchen cannot be read at all (see the module note). */
export async function loadCatalog(client: CatalogSource): Promise<Catalog> {
  // allSettled, not all: one failure must not leave the other nine requests running behind the rejection.
  const settled = await Promise.allSettled([
    readable(() => client.listProviders()),
    readable(() => client.listPrompts()),
    readable(() => client.listTools()),
    readable(() => client.listSkills()),
    readable(() => client.listAgents({ includeRetired: true })), // a retired agent is named so it can be told apart from a missing one
    readable(() => client.listRecipes()),
    readable(() => client.listChannels()),
    readable(() => client.listEnvironments()),
    readable(() => client.listCredentials()),
    readable(() => client.listScenarios()),
  ]);
  const failed = settled.find((r): r is PromiseRejectedResult => r.status === 'rejected');
  if (failed) throw failed.reason;
  const [providers, prompts, tools, skills, agents, recipes, channels, environments, credentials, scenarios] = settled.map((r) => (r as PromiseFulfilledResult<unknown>).value) as [
    Provider[] | undefined, PromptSummary[] | undefined, Tool[] | undefined, SkillSummary[] | undefined, AgentSummary[] | undefined,
    Recipe[] | undefined, NotificationChannel[] | undefined, Environment[] | undefined, Record<string, unknown>[] | undefined, Scenario[] | undefined,
  ];
  const catalog: Catalog = {};
  if (providers) {
    catalog.providers = providers.map((p) => ({
      name: p.name,
      kind: p.kind,
      models: p.models ?? [],
      ...(typeof p.last_test_healthy === 'boolean' ? { healthy: p.last_test_healthy } : {}),
    }));
  }
  if (prompts) {
    const latest: Record<string, number> = Object.create(null);
    for (const p of prompts) latest[p.name] = Math.max(latest[p.name] ?? 0, p.version);
    catalog.prompts = { ...latest };
  }
  if (tools) catalog.tools = tools.map((t) => ({ name: t.name, enabled: t.enabled, capabilities: t.capabilities ?? [] }));
  if (skills) catalog.skills = skills.map((s) => ({ name: s.name, status: s.status }));
  if (agents) catalog.agents = agents.map((a) => ({ name: a.name, status: a.status ?? '' }));
  if (recipes) catalog.recipes = names(recipes);
  if (channels) catalog.channels = names(channels);
  if (environments) catalog.environments = environments.map((e) => e.slug ?? '').filter((s) => s !== '');
  if (credentials) catalog.credentials = names(credentials);
  if (scenarios) catalog.scenarios = { known: true, names: scenarios.map((s) => s.id) };
  return catalog;
}
