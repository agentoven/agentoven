import { Plus, Trash2 } from 'lucide-react';
import type { IngredientKind, Problem } from '../api';
import { useReferenceCatalog } from '../hooks/references';
import {
  ROW_KINDS, cfgString, emptyRow, isInlinePrompt, parseConfig, referencedName, rowUnresolved, siblingRefs, withConfig,
  type IngredientRow, type RefCatalog,
} from '../lib/agentRefs';
import { backendOptions, promptVersions, type Options } from '../lib/references';
import { FieldProblems, ModelReferenceSelect, ReferenceSelect } from './ReferenceSelect';

const field = 'w-full px-3 py-2 rounded-lg bg-[var(--ao-bg)] border border-[var(--ao-border)] text-sm outline-none focus:border-[var(--ao-brand)] transition-colors';
const small = 'block text-xs text-[var(--ao-text-muted)] mb-1';

const asOptions = (values: string[]): Options => values.map((v) => ({ value: v, label: v }));

/** The paths a kind's own dropdowns show problems for; anything else the server says about the row is listed under it. */
const OWN: Partial<Record<IngredientKind, string[]>> = {
  model: ['.config.provider', '.config.model'],
  tool: ['.name', '.config.tool_name'],
  observability: ['.name', '.config.tool_name'],
  prompt: ['.name', '.config.prompt_name', '.config.version'],
  skill: ['.name'],
  scenario: ['.name', '.config.scenario'],
  embedding: ['.config.provider'],
  vectorstore: ['.config.backend'],
  retriever: ['.config.embedding_ref', '.config.vectorstore_ref'],
  data: ['.config.uri'],
};
const isOwn = (kind: IngredientKind, p: Problem) => (OWN[kind] ?? []).some((suffix) => p.path.endsWith(suffix));
const ownProblems = (kind: IngredientKind, ps: Problem[]) => ps.filter((p) => isOwn(kind, p));

/** What is wrong with a row's settings text, shown under it. */
function JsonProblem({ json }: { json: string }) {
  return parseConfig(json).ok ? null : <p role="alert" className="m-0 mt-1 text-xs text-red-300">The settings are not a JSON object, so this agent cannot be saved until they are.</p>;
}

/** The dropdowns of one row, by kind. The settings text below them stays in step: they write keys of it. */
function RowBody({ row, set, catalog, loading, rows, problems }: {
  row: IngredientRow;
  set: (patch: Partial<IngredientRow>) => void;
  catalog: RefCatalog;
  loading?: boolean;
  rows: IngredientRow[];
  problems: Problem[];
}) {
  const parsed = parseConfig(row.configJson);
  const config = parsed.ok ? parsed.config : {};
  const cfg = (patch: Record<string, unknown>, extra?: Partial<IngredientRow>) => set({ configJson: withConfig(row.configJson, patch), ...extra });
  const mine = ownProblems(row.kind, problems);
  const at = (suffix: string) => mine.filter((p) => p.path.endsWith(suffix));
  const disabled = !parsed.ok;
  const label = (what: string) => `${what} of ingredient`;

  const name = (
    <label className="block">
      <span className={small}>Name (what this ingredient is called)</span>
      <input aria-label="Ingredient name" value={row.name} onChange={(e) => set({ name: e.target.value })} className={field} placeholder={row.kind} />
    </label>
  );

  switch (row.kind) {
    case 'model':
      return (
        <div className="space-y-2">
          {name}
          <ModelReferenceSelect label={label('Model')} providers={catalog.providers} loading={loading} disabled={disabled}
            provider={cfgString(config, 'provider')} model={cfgString(config, 'model')} problems={mine}
            onChange={(provider, model) => cfg({ provider, model })} />
        </div>
      );
    case 'tool':
    case 'observability':
      return (
        <ReferenceSelect label={label('Tool')} options={catalog.tools} loading={loading} disabled={disabled} clearable={row.kind === 'observability'}
          value={referencedName(row, config)} problems={mine} placeholder="Choose a tool"
          empty={{ what: 'tools', to: '/tools', link: 'Add a tool' }}
          onChange={(v) => cfg({ tool_name: undefined }, { name: v })} />
      );
    case 'prompt':
      if (isInlinePrompt(config)) return <p className="m-0 text-xs text-[var(--ao-text-muted)]">The prompt is written in this ingredient&apos;s settings, so there is nothing to look up.</p>;
      return (
        <div className="space-y-2">
          <ReferenceSelect label={label('Prompt')} options={catalog.prompts} loading={loading} disabled={disabled}
            value={referencedName(row, config)} problems={mine} placeholder="Choose a prompt"
            empty={{ what: 'prompts', to: '/prompts', link: 'Write a prompt' }}
            onChange={(v) => cfg({ prompt_name: undefined, version: undefined }, { name: v })} />
          {referencedName(row, config) && promptVersions(catalog.prompts, referencedName(row, config)).length > 0 && (
            <label className="block">
              <span className={small}>Version</span>
              <select aria-label="Prompt version" value={config.version === undefined ? '' : String(config.version)} disabled={disabled}
                onChange={(e) => cfg({ version: e.target.value === '' ? undefined : Number(e.target.value) })} className={field}>
                <option value="">Latest</option>
                {promptVersions(catalog.prompts, referencedName(row, config)).map((v) => <option key={v} value={v}>Version {v}</option>)}
                {typeof config.version === 'number' && !promptVersions(catalog.prompts, referencedName(row, config)).includes(config.version) && <option value={config.version}>Version {config.version} (does not exist)</option>}
              </select>
            </label>
          )}
        </div>
      );
    case 'skill':
      return (
        <ReferenceSelect label={label('Skill')} options={catalog.skills} loading={loading} value={row.name} problems={mine} placeholder="Choose a skill"
          empty={{ what: 'skills', to: '/skills', link: 'Register a skill' }} onChange={(v) => set({ name: v })} />
      );
    case 'scenario':
      return (
        <ReferenceSelect label={label('Scenario')} options={catalog.scenarios} loading={loading} disabled={disabled} value={referencedName(row, config)} problems={mine} placeholder="Choose a scenario"
          empty={{ what: 'scenarios', to: '/scenarios', link: 'Open Scenarios' }} onChange={(v) => cfg({ scenario: undefined }, { name: v })} />
      );
    case 'embedding':
      return (
        <div className="space-y-2">
          {name}
          <ReferenceSelect label={label('Embedding provider')} options={catalog.providers?.map((p) => ({ value: p.name, label: p.name, detail: p.kind }))} loading={loading} disabled={disabled}
            value={cfgString(config, 'provider')} problems={at('.provider')} placeholder="Choose a provider"
            empty={{ what: 'providers', to: '/providers', link: 'Add a provider' }} onChange={(v) => cfg({ provider: v })} />
          <label className="block">
            <span className={small}>Embedding model (not checked until it runs)</span>
            <input aria-label="Embedding model" value={cfgString(config, 'model')} disabled={disabled} onChange={(e) => cfg({ model: e.target.value })} className={`${field} font-mono`} placeholder="text-embedding-3-small" />
          </label>
        </div>
      );
    case 'vectorstore':
      return (
        <div className="space-y-2">
          {name}
          <ReferenceSelect label={label('Vector store backend')} options={backendOptions()} disabled={disabled} value={cfgString(config, 'backend') || 'embedded'} problems={at('.backend')}
            onChange={(v) => cfg({ backend: v })} />
          <label className="block">
            <span className={small}>Index (a name inside that store; the embedded store needs none)</span>
            <input aria-label="Vector store index" value={cfgString(config, 'index')} disabled={disabled} onChange={(e) => cfg({ index: e.target.value })} className={`${field} font-mono`} placeholder="default" />
          </label>
        </div>
      );
    case 'retriever': {
      const sib = siblingRefs(rows);
      return (
        <div className="space-y-2">
          {name}
          <p className="m-0 text-xs text-[var(--ao-text-muted)]">A retriever reads from an embedding and a vector store that are ingredients of this same agent. Add those first.</p>
          <div>
            <span className={small}>Embedding</span>
            <ReferenceSelect label={label('Retriever embedding')} options={asOptions(sib.embeddings)} disabled={disabled} clearable value={cfgString(config, 'embedding_ref')} problems={at('.embedding_ref')}
              placeholder="Choose an embedding ingredient" empty={{ what: 'embedding ingredients in this agent' }} onChange={(v) => cfg({ embedding_ref: v })} />
          </div>
          <div>
            <span className={small}>Vector store</span>
            <ReferenceSelect label={label('Retriever vector store')} options={asOptions(sib.vectorstores)} disabled={disabled} clearable value={cfgString(config, 'vectorstore_ref')} problems={at('.vectorstore_ref')}
              placeholder="Choose a vector store ingredient" empty={{ what: 'vector store ingredients in this agent' }} onChange={(v) => cfg({ vectorstore_ref: v })} />
          </div>
        </div>
      );
    }
    case 'data':
      return (
        <div className="space-y-2">
          {name}
          <label className="block">
            <span className={small}>Where the data is (a location, not something registered here)</span>
            <input aria-label="Data uri" value={cfgString(config, 'uri')} disabled={disabled} onChange={(e) => cfg({ uri: e.target.value })} className={`${field} font-mono`} placeholder="s3://bucket/path or https://..." />
          </label>
          <FieldProblems problems={at('.uri')} />
        </div>
      );
    default:
      return null;
  }
}

/**
 * The rows of an agent's ingredients. Every row that names something (a model, a tool, a prompt, a skill, an
 * embedding, a scenario) chooses it from what the kitchen has; what it does not name stays in the settings
 * text. `problemsByRow[i]` are the server's problems for row i, shown on the row.
 */
export function IngredientRowsEditor({ rows, onChange, catalog, loading, problemsByRow = {} }: {
  rows: IngredientRow[];
  onChange: (rows: IngredientRow[]) => void;
  catalog: RefCatalog;
  loading?: boolean;
  problemsByRow?: Record<number, Problem[]>;
}) {
  const update = (i: number, patch: Partial<IngredientRow>) => onChange(rows.map((r, j) => (j === i ? { ...r, ...patch } : r)));
  // Changing the kind starts the settings over: the old kind's keys mean nothing to the new one.
  const changeKind = (i: number, kind: IngredientKind) => update(i, { kind, name: '', configJson: '{}' });
  const kinds = ROW_KINDS.filter((k) => k.value !== 'scenario' || catalog.scenarios !== undefined);

  return (
    <div className="space-y-2">
      {rows.map((row, i) => {
        const problems = problemsByRow[i] ?? [];
        const bad = rowUnresolved(row, i, catalog, rows).length > 0 || problems.some((p) => p.severity !== 'warning');
        return (
          <div key={i} data-row={i} className={`p-3 rounded-lg border bg-[var(--ao-bg)] space-y-2.5 ${bad ? 'border-red-500/50' : 'border-[var(--ao-border)]'}`}>
            <div className="flex flex-wrap gap-2 items-center">
              <select aria-label={`Kind of ingredient ${i + 1}`} value={row.kind} onChange={(e) => changeKind(i, e.target.value as IngredientKind)}
                className="w-36 px-2 py-1.5 rounded bg-[var(--ao-surface)] border border-[var(--ao-border)] text-xs outline-none">
                {kinds.map((k) => <option key={k.value} value={k.value}>{k.label}</option>)}
                {!kinds.some((k) => k.value === row.kind) && <option value={row.kind}>{row.kind}</option>}
              </select>
              <span className="flex-1" />
              <label className="flex items-center gap-1 text-xs">
                <input type="checkbox" checked={row.required} onChange={(e) => update(i, { required: e.target.checked })} /> Required
              </label>
              <button type="button" aria-label={`Remove ingredient ${i + 1}`} onClick={() => onChange(rows.filter((_, j) => j !== i))} className="text-red-400 hover:text-red-300">
                <Trash2 size={14} />
              </button>
            </div>
            <RowBody row={row} set={(patch) => update(i, patch)} catalog={catalog} loading={loading} rows={rows} problems={problems} />
            <details className="text-xs" open={!parseConfig(row.configJson).ok || undefined}>
              <summary className="cursor-pointer text-[var(--ao-text-muted)]">Settings (JSON)</summary>
              <input aria-label={`Settings of ingredient ${i + 1}`} value={row.configJson} onChange={(e) => update(i, { configJson: e.target.value })} placeholder='{"key": "value"}'
                className="mt-1.5 w-full px-2 py-1.5 rounded bg-[var(--ao-surface)] border border-[var(--ao-border)] text-xs font-mono outline-none" />
            </details>
            <JsonProblem json={row.configJson} />
            <FieldProblems problems={problems.filter((p) => !isOwn(row.kind, p))} />
          </div>
        );
      })}
      <button type="button" onClick={() => onChange([...rows, emptyRow()])} className="text-xs text-[var(--ao-brand)] hover:text-[var(--ao-brand-light)] flex items-center gap-1">
        <Plus size={12} /> Add ingredient
      </button>
    </div>
  );
}

/** The rows, fed from the kitchen's own lists. */
export function IngredientsEditor(props: { rows: IngredientRow[]; onChange: (rows: IngredientRow[]) => void; problemsByRow?: Record<number, Problem[]> }) {
  const { catalog, loading } = useReferenceCatalog();
  return <IngredientRowsEditor {...props} catalog={catalog} loading={loading} />;
}
