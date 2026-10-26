import { useRef, useState } from 'react';
import { Link, useNavigate } from 'react-router-dom';
import { Plus, Upload } from 'lucide-react';
import { agents, recipes, type Recipe } from '../../api';
import { useAPI } from '../../hooks';
import { ErrorBanner, PageHeader } from '../../components/UI';
import { metaOfStep } from '../../components/recipes/stepMeta';
import { BLANK, TEMPLATES, allNodes, compile, stepCount, type FlowNode } from '../../lib/recipeFlow';

const NAME_RE = /^[a-z0-9][a-z0-9_-]{1,62}$/;
const nameError = (n: string): string | null => {
  const t = n.trim();
  if (!t) return 'Give the recipe a name first.';
  if (!NAME_RE.test(t)) return "Use 2 to 63 characters: lowercase letters, numbers, '-' or '_'.";
  return null;
};

function MiniFlow({ flow }: { flow: FlowNode[] }) {
  const nodes = allNodes(flow);
  return (
    <div className="flex items-center flex-wrap gap-y-1.5">
      {nodes.map((n, i) => {
        const m = metaOfStep(n.step);
        const Icon = m.icon;
        return (
          <span key={n.id} className="inline-flex items-center">
            <span className="w-[30px] h-[30px] rounded-full grid place-items-center" style={{ background: m.bg, color: m.color }}><Icon size={15} /></span>
            {i < nodes.length - 1 && <span aria-hidden="true" className="w-3.5 h-0.5 bg-slate-600" />}
          </span>
        );
      })}
    </div>
  );
}

/** Picks the quickest way to start. Nothing is saved until a way is chosen, and every step stays editable. */
export default function RecipeNew() {
  const navigate = useNavigate();
  const { data: agentList } = useAPI(() => agents.list());
  const [name, setName] = useState('');
  const [description, setDescription] = useState('');
  const [busy, setBusy] = useState(false);
  const [error, setError] = useState('');
  const [touched, setTouched] = useState(false);
  const fileRef = useRef<HTMLInputElement>(null);
  const firstAgent = (agentList ?? []).find((a) => a.status === 'ready')?.name ?? '';
  const problem = nameError(name);

  const create = async (steps: Recipe['steps']) => {
    setTouched(true);
    if (problem) return;
    setBusy(true); setError('');
    try {
      await recipes.create({ name: name.trim(), description: description.trim(), steps });
      navigate(`/recipes/${encodeURIComponent(name.trim())}`);
    } catch (e) {
      setError(e instanceof Error ? e.message : 'The recipe could not be created.');
      setBusy(false);
    }
  };

  const importFile = async (file: File | undefined) => {
    if (!file) return;
    setError('');
    try {
      const parsed = JSON.parse(await file.text()) as Partial<Recipe>;
      if (!Array.isArray(parsed.steps)) throw new Error('This file has no steps.');
      if (!name.trim() && parsed.name) setName(parsed.name);
      setTouched(true);
      const finalName = (name.trim() || parsed.name || '').trim();
      const e = nameError(finalName);
      if (e) { setError(e); return; }
      setBusy(true);
      await recipes.create({ name: finalName, description: parsed.description ?? description.trim(), steps: parsed.steps });
      navigate(`/recipes/${encodeURIComponent(finalName)}`);
    } catch (e) {
      setError(e instanceof Error ? `That file could not be imported: ${e.message}` : 'That file could not be imported.');
      setBusy(false);
    }
  };

  return (
    <div>
      <PageHeader title="New recipe" description="Name it, then pick the easiest way to start. Every step can be changed in the builder before anything runs." />
      <div className="px-4 sm:px-8 pb-10 max-w-5xl space-y-7">
        {error && <ErrorBanner message={error} />}
        <div className="grid gap-4 grid-cols-1 sm:grid-cols-2 max-w-3xl">
          <label className="flex flex-col gap-1.5 text-[13px] font-medium text-slate-300">Name
            <input value={name} onChange={(e) => setName(e.target.value)} onBlur={() => setTouched(true)} placeholder="refund-review" aria-invalid={touched && !!problem}
              className="h-10 px-3 rounded-[10px] bg-[var(--ao-surface)] border border-[var(--ao-border)] text-sm outline-none focus:border-[var(--ao-brand)]" />
            {touched && problem && <span className="text-xs font-normal text-amber-300">{problem}</span>}
          </label>
          <label className="flex flex-col gap-1.5 text-[13px] font-medium text-slate-300">What it is for (optional)
            <input value={description} onChange={(e) => setDescription(e.target.value)} placeholder="Triage a ticket, ask a person for large refunds"
              className="h-10 px-3 rounded-[10px] bg-[var(--ao-surface)] border border-[var(--ao-border)] text-sm outline-none focus:border-[var(--ao-brand)]" />
          </label>
        </div>

        <section aria-label="Templates">
          <h2 className="text-[17px] font-semibold m-0 mb-3">Start from a template</h2>
          <div className="grid gap-3.5 grid-cols-1 md:grid-cols-3">
            {TEMPLATES.map((t) => {
              const flow = t.build(firstAgent);
              return (
                <button key={t.id} type="button" disabled={busy} onClick={() => create(compile(flow) as unknown as Recipe['steps'])}
                  className="flex flex-col gap-3 p-[18px] text-left rounded-2xl bg-[var(--ao-surface)] border border-[var(--ao-border)] hover:border-[var(--ao-brand-light)] disabled:opacity-60">
                  <MiniFlow flow={flow} />
                  <span>
                    <span className="block text-[15px] font-semibold">{t.name}</span>
                    <span className="block mt-1 text-[13px] leading-relaxed text-[var(--ao-text-muted)]">{t.body}</span>
                  </span>
                  <span className="text-[12.5px] text-[var(--ao-text-muted)]">{stepCount(flow)} step{stepCount(flow) === 1 ? '' : 's'}, then change anything you like</span>
                </button>
              );
            })}
          </div>
        </section>

        <section aria-label="Other ways" className="grid gap-3.5 grid-cols-1 md:grid-cols-2">
          <button type="button" disabled={busy} onClick={() => create(compile(BLANK()) as unknown as Recipe['steps'])}
            className="flex items-center gap-3.5 px-[18px] py-4 text-left rounded-2xl border border-dashed border-slate-600 hover:border-[var(--ao-brand-light)] disabled:opacity-60">
            <span className="w-9 h-9 rounded-[10px] grid place-items-center bg-[var(--ao-surface)] text-slate-300"><Plus size={18} /></span>
            <span><span className="block text-[15px] font-semibold">Start blank</span><span className="block mt-0.5 text-[13px] text-[var(--ao-text-muted)]">An empty flow, then add steps one at a time.</span></span>
          </button>
          <button type="button" disabled={busy} onClick={() => fileRef.current?.click()}
            className="flex items-center gap-3.5 px-[18px] py-4 text-left rounded-2xl border border-dashed border-slate-600 hover:border-[var(--ao-brand-light)] disabled:opacity-60">
            <span className="w-9 h-9 rounded-[10px] grid place-items-center bg-[var(--ao-surface)] text-slate-300"><Upload size={18} /></span>
            <span><span className="block text-[15px] font-semibold">Import a file</span><span className="block mt-0.5 text-[13px] text-[var(--ao-text-muted)]">Bring a recipe written as JSON.</span></span>
          </button>
          <input ref={fileRef} type="file" accept="application/json,.json" className="hidden" aria-label="Choose a recipe file" onChange={(e) => { void importFile(e.target.files?.[0]); e.target.value = ''; }} />
        </section>
        <p className="text-sm text-[var(--ao-text-muted)] m-0"><Link to="/recipes" className="text-[var(--ao-brand-light)] hover:underline">Back to recipes</Link></p>
      </div>
    </div>
  );
}
