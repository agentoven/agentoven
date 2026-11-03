// The references inside an agent's form, and the rule that keeps a form from being saved while one of them
// names nothing. Pure: the form feeds it what it holds and the lists the dropdowns were built from.
//
// An ingredient row keeps its settings as the JSON text the person can see; the dropdowns read and write
// keys of that text (config.provider, config.model, ...), so the text stays the one source of truth and
// nothing the form does not know about is lost.

import type { Ingredient, IngredientKind, Problem } from '../api.ts';
import {
  VECTOR_BACKENDS, rowOfPath, unresolved, unresolvedMany, unresolvedModel,
  type ModelChoice, type Options, type ProviderLike, type Unresolved,
} from './references.ts';

export interface IngredientRow {
  name: string;
  kind: IngredientKind;
  configJson: string;
  required: boolean;
}

/** The kinds a row can be, in the order they are offered. `scenario` is Pro's and is offered only where scenarios can be listed. */
export const ROW_KINDS: { value: IngredientKind; label: string }[] = [
  { value: 'model', label: 'Model' },
  { value: 'tool', label: 'Tool' },
  { value: 'prompt', label: 'Prompt' },
  { value: 'skill', label: 'Skill' },
  { value: 'data', label: 'Data' },
  { value: 'embedding', label: 'Embedding' },
  { value: 'vectorstore', label: 'Vector store' },
  { value: 'retriever', label: 'Retriever' },
  { value: 'observability', label: 'Observability' },
  { value: 'scenario', label: 'Scenario' },
];

export const emptyRow = (kind: IngredientKind = 'tool'): IngredientRow => ({ name: '', kind, configJson: '{}', required: false });

export const rowFromIngredient = (i: Ingredient): IngredientRow => ({
  name: i.name,
  kind: i.kind,
  configJson: JSON.stringify(i.config ?? {}),
  required: i.required ?? false,
});

export type ParsedConfig = { ok: true; config: Record<string, unknown> } | { ok: false };

/** The settings of a row, or `ok: false` when the text is not a JSON object (the form refuses to save it, instead of silently saving `{}`). */
export function parseConfig(json: string): ParsedConfig {
  try {
    const v: unknown = JSON.parse(json.trim() === '' ? '{}' : json);
    return v && typeof v === 'object' && !Array.isArray(v) ? { ok: true, config: v as Record<string, unknown> } : { ok: false };
  } catch {
    return { ok: false };
  }
}

/** The settings text with `patch` laid over it; a key set to undefined or "" is removed. Text that is not valid JSON is left alone. */
export function withConfig(json: string, patch: Record<string, unknown>): string {
  const parsed = parseConfig(json);
  if (!parsed.ok) return json;
  const next: Record<string, unknown> = { ...parsed.config };
  for (const [k, v] of Object.entries(patch)) {
    if (v === undefined || v === '') delete next[k];
    else next[k] = v;
  }
  return JSON.stringify(next);
}

export const cfgString = (config: Record<string, unknown>, key: string): string => (typeof config[key] === 'string' ? (config[key] as string) : '');

/** Whether a prompt row is written in place (text or template) rather than looked up by name. */
export const isInlinePrompt = (config: Record<string, unknown>): boolean => cfgString(config, 'text') !== '' || cfgString(config, 'template') !== '';

/** The name a row refers to, for the kinds whose name is a reference: the tool, prompt, skill or scenario it uses. */
export function referencedName(row: IngredientRow, config: Record<string, unknown>): string {
  switch (row.kind) {
    case 'tool':
    case 'observability':
      return cfgString(config, 'tool_name') || row.name;
    case 'prompt':
      return isInlinePrompt(config) ? '' : cfgString(config, 'prompt_name') || row.name;
    case 'scenario':
      return cfgString(config, 'scenario') || row.name;
    case 'skill':
      return row.name;
    default:
      return '';
  }
}

/** What the agent form's dropdowns were built from. A list that is undefined could not be read and is not checked. */
export interface RefCatalog {
  providers?: ProviderLike[];
  tools?: Options;
  skills?: Options;
  agents?: Options;
  prompts?: Options;
  scenarios?: Options;
}

/** One thing in a form that names nothing, with where it is. */
export interface FormUnresolved { where: string; value: string; why: string; suggestions: string[] }

const found = (where: string, u: Unresolved | null): FormUnresolved[] => (u ? [{ where, value: u.value, why: u.why, suggestions: u.suggestions }] : []);

/** The names a built-in retriever may point at: the embedding and vector store ingredients of the same agent. */
export function siblingRefs(rows: IngredientRow[]): { embeddings: string[]; vectorstores: string[] } {
  const embeddings: string[] = [];
  const vectorstores: string[] = [];
  for (const r of rows) {
    const p = parseConfig(r.configJson);
    if (!p.ok) continue;
    if (r.kind === 'embedding') { const v = cfgString(p.config, 'provider'); if (v) embeddings.push(v); }
    if (r.kind === 'vectorstore') {
      const backend = cfgString(p.config, 'backend') || 'embedded';
      vectorstores.push(backend);
      const index = cfgString(p.config, 'index');
      if (index) vectorstores.push(index);
    }
  }
  return { embeddings: [...new Set(embeddings)], vectorstores: [...new Set(vectorstores)] };
}

const asOptions = (values: string[]): Options => values.map((v) => ({ value: v, label: v }));

/** What is wrong with one ingredient row: a reference that names nothing, or settings that are not JSON. */
export function rowUnresolved(row: IngredientRow, i: number, cat: RefCatalog, rows: IngredientRow[]): FormUnresolved[] {
  const where = `Ingredient ${i + 1} (${row.name || row.kind})`;
  const parsed = parseConfig(row.configJson);
  if (!parsed.ok) return [{ where, value: 'settings', why: 'are not valid JSON', suggestions: [] }];
  const c = parsed.config;
  switch (row.kind) {
    case 'model': {
      const m = unresolvedModel({ provider: cfgString(c, 'provider'), model: cfgString(c, 'model') }, cat.providers);
      return m ? [{ where: `${where} ${m.field}`, value: m.value, why: m.why, suggestions: m.suggestions }] : [];
    }
    case 'tool':
    case 'observability':
      return found(`${where} tool`, unresolved(referencedName(row, c), cat.tools));
    case 'prompt':
      return found(`${where} prompt`, unresolved(referencedName(row, c), cat.prompts));
    case 'skill':
      return found(`${where} skill`, unresolved(row.name, cat.skills));
    case 'scenario':
      return found(`${where} scenario`, unresolved(referencedName(row, c), cat.scenarios));
    case 'embedding':
      return found(`${where} provider`, unresolved(cfgString(c, 'provider'), cat.providers?.map((p) => ({ value: p.name, label: p.name }))));
    case 'vectorstore': {
      const backend = cfgString(c, 'backend');
      return backend && !VECTOR_BACKENDS.includes(backend) ? found(`${where} backend`, unresolved(backend, asOptions(VECTOR_BACKENDS))) : [];
    }
    case 'retriever': {
      const provider = cfgString(c, 'provider');
      if (provider && provider !== 'built-in') return []; // an external RAG provider keeps its own
      const sib = siblingRefs(rows);
      return [
        ...found(`${where} embedding`, unresolved(cfgString(c, 'embedding_ref'), asOptions(sib.embeddings))),
        ...found(`${where} vector store`, unresolved(cfgString(c, 'vectorstore_ref'), asOptions(sib.vectorstores))),
      ];
    }
    default:
      return [];
  }
}

export interface AgentRefs {
  model: ModelChoice;
  backup: ModelChoice;
  summary: ModelChoice;
  skills: string[];
  approvalTools: string[];
  subagents: string[];
  ingredients: IngredientRow[];
}

const modelFound = (where: string, c: ModelChoice, providers: ProviderLike[] | undefined, providerOptional = false): FormUnresolved[] => {
  const m = unresolvedModel(c, providers, providerOptional);
  return m ? [{ where: `${where} ${m.field}`, value: m.value, why: m.why, suggestions: m.suggestions }] : [];
};

/**
 * Everything in an agent's form that names nothing the kitchen has. The form cannot be saved while this is
 * not empty: the person replaces or removes each one. A list that could not be read contributes nothing, so a
 * failed request never locks a form (the server still refuses what is wrong when it is saved).
 */
export function agentUnresolved(refs: AgentRefs, cat: RefCatalog): FormUnresolved[] {
  return [
    ...modelFound('Model', refs.model, cat.providers),
    ...modelFound('Backup', refs.backup, cat.providers),
    ...modelFound('Summary model', refs.summary, cat.providers, true),
    ...unresolvedMany(refs.skills, cat.skills).map((u) => ({ where: 'Skills', ...u })),
    ...unresolvedMany(refs.approvalTools, cat.tools).map((u) => ({ where: 'Approval tools', ...u })),
    ...unresolvedMany(refs.subagents, cat.agents).map((u) => ({ where: 'Subagents', ...u })),
    ...refs.ingredients.flatMap((r, i) => rowUnresolved(r, i, cat, refs.ingredients)),
  ];
}

/** One line for the list beside a disabled Save button. */
export const describeUnresolved = (u: FormUnresolved): string => `${u.where}: ${u.value} ${u.why}`;

/** A row nobody filled in: it names nothing and sets nothing, so it is left out rather than sent as an empty ingredient. */
export function isBlankRow(row: IngredientRow): boolean {
  const p = parseConfig(row.configJson);
  if (!p.ok) return false; // the person typed something: refuse it, do not drop it
  const c = p.config;
  switch (row.kind) {
    case 'model':
    case 'embedding':
      return cfgString(c, 'provider') === '' && row.name === '';
    case 'data':
      return cfgString(c, 'uri') === '' && row.name === '';
    case 'vectorstore':
    case 'retriever':
      return false; // they have a meaning with nothing set (the embedded store, the built-in retriever)
    case 'prompt':
      return !isInlinePrompt(c) && referencedName(row, c) === '';
    default:
      return referencedName(row, c) === '';
  }
}

/**
 * The ingredients to send, and for each form row the index it was sent at (-1 for a row left out), so a
 * problem the server reports at `ingredients[3]` can be shown on the row that made it. `leading` is how many
 * ingredients the form puts in front of the rows (the system prompt).
 */
export function buildIngredients(rows: IngredientRow[], leading: number): { ingredients: Partial<Ingredient>[]; sentAt: number[] } {
  const ingredients: Partial<Ingredient>[] = [];
  const sentAt = rows.map((r) => {
    if (isBlankRow(r)) return -1;
    const p = parseConfig(r.configJson);
    ingredients.push({ name: r.name || r.kind, kind: r.kind, config: p.ok ? p.config : {}, required: r.required });
    return leading + ingredients.length - 1;
  });
  return { ingredients, sentAt };
}

/** The server's problems for each ingredient row: a problem at `ingredients[3]` goes to the row that was sent as index 3. */
export function rowProblems(problems: Problem[], sentAt: number[]): Record<number, Problem[]> {
  const out: Record<number, Problem[]> = {};
  for (const p of problems) {
    const row = rowOfPath(p.path, 'ingredients', sentAt);
    if (row >= 0) (out[row] ??= []).push(p);
  }
  return out;
}
