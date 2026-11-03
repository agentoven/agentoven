// One hook per kind of thing a configuration may name. Each reads the kitchen's own list once, shares it
// with every field that asks, and hands back options in the shape ReferenceSelect shows.
//
// Caching: a list is kept per kitchen (the kitchen is part of the key, so switching kitchens never shows
// another's names) and read again when a field mounts and what is kept is older than STALE_MS, so a provider
// registered a moment ago is there when the form opens. `refetch` (or invalidateReferences) forces a read
// after something was made in place, such as a provider added from the agent form.
//
// A list that cannot be had (a route this edition does not have, a role that may not read it, a failed
// request) is `undefined`, never an empty array: a field fed by it says it cannot check, and nothing is
// reported as missing because of it.

import { useCallback, useEffect, useMemo, useSyncExternalStore } from 'react';
import { agents, notificationChannels, prompts, providers, recipes, referenceLists, skills, tools } from '../api';
import type { Agent, MCPTool, ModelProvider, NotificationChannel, Prompt, Recipe, Skill } from '../api';
import {
  agentOptions, channelOptions, credentialOptions, environmentOptions, promptOptions, providerOptions, recipeOptions,
  roleOptions, scenarioOptions, skillOptions, toolOptions,
  type RefOption,
} from '../lib/references';
import type { RefCatalog } from '../lib/agentRefs';

const STALE_MS = 5000;

type Resource = 'providers' | 'prompts' | 'tools' | 'skills' | 'agents' | 'recipes' | 'channels' | 'environments' | 'credentials' | 'scenarios';

interface Entry {
  at: number;
  /** The list; null when this edition or role has no such list. */
  data?: unknown[] | null;
  error?: string;
  inflight?: boolean;
}

const cache = new Map<string, Entry>();
const listeners = new Set<() => void>();
const notify = () => listeners.forEach((l) => l());

function kitchenKey(): string {
  try { return window.localStorage.getItem('ao_kitchen') || 'default'; } catch { return 'default'; }
}
const keyOf = (r: Resource) => `${kitchenKey()}\u0000${r}`;

const FETCHERS: Record<Resource, () => Promise<unknown[] | null>> = {
  providers: () => providers.list(),
  prompts: () => prompts.list(),
  tools: () => tools.list(),
  skills: () => skills.list(),
  agents: () => agents.list(),
  recipes: () => recipes.list(),
  channels: () => notificationChannels.list(),
  environments: () => referenceLists.environments(),
  credentials: () => referenceLists.credentials(),
  scenarios: () => referenceLists.scenarios(),
};

function load(resource: Resource, force: boolean) {
  const key = keyOf(resource);
  const held = cache.get(key);
  if (held?.inflight) return;
  if (!force && held && Date.now() - held.at < STALE_MS) return;
  cache.set(key, { ...held, at: held?.at ?? 0, inflight: true });
  notify();
  FETCHERS[resource]().then(
    (data) => cache.set(key, { at: Date.now(), data: data ?? null }),
    (e: unknown) => cache.set(key, { at: Date.now(), data: held?.data, error: e instanceof Error ? e.message : 'could not be read' }),
  ).finally(notify);
}

/** Forget what was read (one kind, or all of them) and read again where a field is showing. */
export function invalidateReferences(resource?: Resource) {
  for (const key of [...cache.keys()]) if (!resource || key.endsWith(`\u0000${resource}`)) cache.delete(key);
  notify();
}

export interface ReferenceList<T> {
  /** The list, or undefined while it is unknown: loading, absent for this edition or role, or failed. */
  data: T[] | undefined;
  loading: boolean;
  /** This edition or this role has no such list: the field should say so rather than offer an empty one. */
  absent: boolean;
  error?: string;
  refetch: () => void;
}

function useReference<T>(resource: Resource): ReferenceList<T> {
  const key = keyOf(resource);
  const subscribe = useCallback((l: () => void) => { listeners.add(l); return () => { listeners.delete(l); }; }, []);
  const entry = useSyncExternalStore(subscribe, () => cache.get(key));
  useEffect(() => { load(resource, false); }, [resource, key]);
  const refetch = useCallback(() => load(resource, true), [resource]);
  return {
    data: entry?.data ? (entry.data as T[]) : undefined,
    loading: !entry || (entry.data === undefined && !entry.error),
    absent: entry?.data === null,
    error: entry?.error,
    refetch,
  };
}

export interface ReferenceOptions<T = unknown> extends ReferenceList<T> {
  /** Options for a ReferenceSelect; undefined while unknown. */
  options: RefOption[] | undefined;
}

function useWithOptions<T>(list: ReferenceList<T>, build: (items: T[]) => RefOption[]): ReferenceOptions<T> {
  const { data } = list;
  const options = useMemo(() => (data ? build(data) : undefined), [data, build]);
  return { ...list, options };
}

const buildProviders = (p: ModelProvider[]) => providerOptions(p);
/** Providers with their models: the provider dropdown, then the model dropdown of the one chosen. */
export function useProviderOptions(): ReferenceOptions<ModelProvider> {
  return useWithOptions(useReference<ModelProvider>('providers'), buildProviders);
}

export function usePromptOptions(): ReferenceOptions<Prompt> {
  return useWithOptions(useReference<Prompt>('prompts'), promptOptions);
}

/** `need` is what the field uses a tool for: "tool" to call it, "notify" to send through it. */
export function useToolOptions(need: 'tool' | 'notify' | '' = 'tool'): ReferenceOptions<MCPTool> {
  const build = useCallback((t: MCPTool[]) => toolOptions(t, need), [need]);
  return useWithOptions(useReference<MCPTool>('tools'), build);
}

export function useSkillOptions(): ReferenceOptions<Skill> {
  return useWithOptions(useReference<Skill>('skills'), skillOptions);
}

/** The agents a field may name: `self` is left out and a retired agent is shown but cannot be picked. */
export function useAgentOptions(self = ''): ReferenceOptions<Agent> {
  const build = useCallback((a: Agent[]) => agentOptions(a, self), [self]);
  return useWithOptions(useReference<Agent>('agents'), build);
}

export function useRecipeOptions(self = ''): ReferenceOptions<Recipe> {
  const build = useCallback((r: Recipe[]) => recipeOptions(r, self), [self]);
  return useWithOptions(useReference<Recipe>('recipes'), build);
}

export function useChannelOptions(): ReferenceOptions<NotificationChannel> {
  return useWithOptions(useReference<NotificationChannel>('channels'), channelOptions);
}

export function useEnvironmentOptions(): ReferenceOptions<{ slug: string; name?: string }> {
  return useWithOptions(useReference<{ slug: string; name?: string }>('environments'), environmentOptions);
}

export function useCredentialOptions(): ReferenceOptions<{ name: string }> {
  return useWithOptions(useReference<{ name: string }>('credentials'), credentialOptions);
}

/** Pro only: where the route is absent `absent` is true and the field is left out. */
export function useScenarioOptions(): ReferenceOptions<{ id: string; name?: string }> {
  return useWithOptions(useReference<{ id: string; name?: string }>('scenarios'), scenarioOptions);
}

const STATIC_ROLES = roleOptions();
/** The roles a recipe's approval may name. They are fixed, so they are always known. */
export function useRoleOptions(): { options: RefOption[] } {
  return { options: STATIC_ROLES };
}

/** Everything an agent's form can name, as one catalog (see lib/agentRefs). `self` is left out of the agents it may delegate to. */
export function useReferenceCatalog(self = ''): { catalog: RefCatalog; loading: boolean } {
  const providers = useProviderOptions();
  const tools = useToolOptions('tool');
  const skills = useSkillOptions();
  const agents = useAgentOptions(self);
  const prompts = usePromptOptions();
  const scenarios = useScenarioOptions();
  const catalog = useMemo<RefCatalog>(() => ({
    providers: providers.data,
    tools: tools.options,
    skills: skills.options,
    agents: agents.options,
    prompts: prompts.options,
    scenarios: scenarios.options,
  }), [providers.data, tools.options, skills.options, agents.options, prompts.options, scenarios.options]);
  return { catalog, loading: providers.loading || tools.loading || skills.loading || agents.loading || prompts.loading };
}
