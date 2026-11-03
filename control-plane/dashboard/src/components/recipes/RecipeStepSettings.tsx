import { useState } from 'react';
import { Link } from 'react-router-dom';
import { Plus, X } from 'lucide-react';
import type { WireStep } from '../../lib/recipeFlow';
import { RecipeField } from '../ReferenceFields';

const field = 'w-full h-10 px-3 rounded-[10px] bg-[var(--ao-bg)] border border-[var(--ao-border)] text-sm outline-none focus:border-[var(--ao-brand)]';
const label = 'flex flex-col gap-1.5 text-[13px] font-medium text-slate-300';

/** A recipe a "run a recipe" step may pick. */
export interface RecipeOption { name: string; description?: string; steps: number }

type Mapping = Record<string, string> | undefined;

/**
 * Rows of "name" and "where it comes from". A row is kept here until both halves are filled in, then it joins
 * the step; an empty list removes the mapping, which gives the default behaviour.
 */
function MappingEditor({ title, hint, from, to, placeholderFrom, placeholderTo, value, onChange }: {
  title: string; hint: string; from: string; to: string; placeholderFrom: string; placeholderTo: string;
  value: Mapping; onChange: (v: Mapping) => void;
}) {
  const [rows, setRows] = useState<[string, string][]>(() => Object.entries(value ?? {}));
  const emit = (next: [string, string][]) => {
    setRows(next);
    const m = Object.fromEntries(next.filter(([k, v]) => k.trim() && v.trim()).map(([k, v]) => [k.trim(), v.trim()]));
    onChange(Object.keys(m).length ? m : undefined);
  };
  return (
    <fieldset className="border-0 m-0 p-0 min-w-0 flex flex-col gap-2">
      <legend className="p-0 mb-1 text-[13px] font-medium text-slate-300">{title}</legend>
      <p className="m-0 text-xs text-[var(--ao-text-muted)] leading-relaxed">{hint}</p>
      {rows.length > 0 && (
        <div className="grid grid-cols-[1fr_1fr_28px] gap-x-2 gap-y-1.5 items-center">
          <span className="text-[11.5px] uppercase tracking-wider text-[var(--ao-text-muted)]">{to}</span>
          <span className="text-[11.5px] uppercase tracking-wider text-[var(--ao-text-muted)]">{from}</span>
          <span />
          {rows.map(([k, v], i) => (
            <div key={i} className="contents">
              <input aria-label={`${title}: ${to} ${i + 1}`} className={`${field} !h-9 font-mono text-[12.5px]`} value={k} placeholder={placeholderTo}
                onChange={(e) => emit(rows.map((r, j) => (j === i ? [e.target.value, r[1]] : r)))} />
              <input aria-label={`${title}: ${from} ${i + 1}`} className={`${field} !h-9 font-mono text-[12.5px]`} value={v} placeholder={placeholderFrom}
                onChange={(e) => emit(rows.map((r, j) => (j === i ? [r[0], e.target.value] : r)))} />
              <button type="button" aria-label={`Remove row ${i + 1}`} onClick={() => emit(rows.filter((_, j) => j !== i))}
                className="w-7 h-7 grid place-items-center rounded-md text-[var(--ao-text-muted)] hover:text-red-300 hover:bg-[var(--ao-surface-hover)]"><X size={14} /></button>
            </div>
          ))}
        </div>
      )}
      <button type="button" onClick={() => setRows([...rows, ['', '']])}
        className="self-start inline-flex items-center gap-1.5 h-8 px-2.5 rounded-lg text-[13px] font-medium text-slate-300 border border-dashed border-slate-600 hover:border-[var(--ao-brand-light)]">
        <Plus size={13} /> Add a row
      </button>
    </fieldset>
  );
}

/** Settings of a step that runs another recipe. */
export default function RecipeStepSettings({ step, recipes, self, onChange }: {
  step: WireStep;
  recipes: RecipeOption[];
  /** The recipe being edited, which it may not run. */
  self: string;
  onChange: (patch: Partial<WireStep>) => void;
}) {
  const ref = String(step.recipe_ref ?? '');
  const picked = recipes.find((r) => r.name === ref);
  return (
    <>
      <div className={label}>Recipe to run
        <RecipeField label="Recipe to run" self={self} value={ref} onChange={(v) => onChange({ recipe_ref: v })} />
        {picked ? (
          <span className="text-xs font-normal text-[var(--ao-text-muted)]">
            {picked.description ? `${picked.description} · ` : ''}{picked.steps} step{picked.steps === 1 ? '' : 's'} ·{' '}
            <Link to={`/recipes/${encodeURIComponent(picked.name)}`} className="text-[var(--ao-brand-light)] hover:underline">Open it</Link>
          </span>
        ) : (
          <span className="text-xs font-normal text-[var(--ao-text-muted)]">It runs to the end before the next step starts, and this step fails if it fails.</span>
        )}
      </div>

      <MappingEditor
        title="What it receives"
        hint="Nothing set: it gets this recipe's input as parent_input, and what the step before produced under that step's name. Set rows to pass only what you choose."
        to="Name inside" from="Take it from" placeholderTo="ticket" placeholderFrom="parent_input.ticket"
        value={step.input_mapping as Mapping}
        onChange={(m) => onChange({ input_mapping: m })}
      />
      <MappingEditor
        title="What comes back"
        hint="Nothing set: everything it produced, under each of its step names. Set rows to take only what you need."
        to="Name here" from="Take it from" placeholderTo="reply" placeholderFrom="write-reply.text"
        value={step.output_mapping as Mapping}
        onChange={(m) => onChange({ output_mapping: m })}
      />
    </>
  );
}
