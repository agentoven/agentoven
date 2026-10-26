import { useEffect, useMemo, useState } from 'react';
import { Link, useNavigate } from 'react-router-dom';
import { BookOpen, Play, Plus, Search, Trash2, UserCheck } from 'lucide-react';
import { useAPI } from '../hooks';
import { recipeRuns, recipes, type Recipe, type RecipeRun } from '../api';
import { Button, EmptyState, ErrorBanner, PageHeader, Spinner } from '../components/UI';
import RunRecipeModal from '../components/recipes/RunRecipeModal';
import { PALETTE, metaOfStep } from '../components/recipes/stepMeta';
import { ActionDropdown } from '../components/ActionDropdown';
import type { WireStep } from '../lib/recipeFlow';

const COLS = 'minmax(170px, 1.2fr) minmax(280px, 2fr) 90px minmax(170px, 1fr) 190px';

function ago(iso?: string): string {
  if (!iso) return '';
  const s = Math.max(0, (Date.now() - new Date(iso).getTime()) / 1000);
  if (s < 60) return 'Just now';
  if (s < 3600) return `${Math.floor(s / 60)} minutes ago`;
  if (s < 86400) return `${Math.floor(s / 3600)} hours ago`;
  return `${Math.floor(s / 86400)} days ago`;
}

function lastRun(runs: RecipeRun[] | undefined): { text: string; dot: string; color: string } {
  if (!runs || runs.length === 0) return { text: 'Never run', dot: '#64748b', color: '#94a3b8' };
  const r = [...runs].sort((a, b) => (b.started_at ?? '').localeCompare(a.started_at ?? ''))[0];
  if (r.status === 'completed') return { text: ago(r.started_at), dot: '#4ade80', color: '#cbd5e1' };
  if (r.status === 'failed') return { text: `Failed ${ago(r.started_at).toLowerCase()}`, dot: '#f87171', color: '#fca5a5' };
  if (r.status === 'paused') return { text: 'Waiting for approval', dot: '#fbbf24', color: '#fde68a' };
  if (r.status === 'canceled') return { text: `Canceled ${ago(r.started_at).toLowerCase()}`, dot: '#94a3b8', color: '#94a3b8' };
  return { text: 'Running now', dot: '#60a5fa', color: '#93c5fd' };
}

function Flow({ steps }: { steps: WireStep[] }) {
  const shown = steps.slice(0, 8);
  return (
    <div className="flex items-center flex-wrap gap-y-1.5 min-w-0">
      {shown.map((s, i) => {
        const m = metaOfStep(s);
        const Icon = m.icon;
        return (
          <span key={s.name} className="inline-flex items-center">
            <span title={m.label} role="img" aria-label={m.label} className="w-[30px] h-[30px] rounded-full grid place-items-center" style={{ background: m.bg, color: m.color }}><Icon size={15} /></span>
            {i < shown.length - 1 && <span aria-hidden="true" className="w-3.5 h-0.5 bg-slate-600" />}
          </span>
        );
      })}
      {steps.length > shown.length && <span className="ml-2 text-xs text-[var(--ao-text-muted)]">+{steps.length - shown.length}</span>}
      <span className="ml-2.5 text-[12.5px] text-[var(--ao-text-muted)]">{steps.length === 0 ? 'No steps yet' : `${steps.length} step${steps.length === 1 ? '' : 's'}`}</span>
    </div>
  );
}

export default function Recipes() {
  const canWrite = true;
  const navigate = useNavigate();
  const { data, loading, error, refetch } = useAPI(() => recipes.list());
  const [runs, setRuns] = useState<Record<string, RecipeRun[]>>({});
  const [query, setQuery] = useState('');
  const [problem, setProblem] = useState('');
  const [running, setRunning] = useState<string | null>(null);

  // Each recipe's runs, so a row can say when it last ran and a strip can say what is waiting.
  const names = (data ?? []).map((r) => r.name).join('|');
  useEffect(() => {
    let live = true;
    const list = names ? names.split('|') : [];
    Promise.all(list.map(async (n) => [n, await recipeRuns.list(n).catch(() => [] as RecipeRun[])] as const)).then((entries) => {
      if (live) setRuns(Object.fromEntries(entries));
    });
    return () => { live = false; };
  }, [names]);

  const waiting = Object.values(runs).flat().filter((r) => r.status === 'paused').length;
  const shown = useMemo(() => {
    const q = query.trim().toLowerCase();
    return (data ?? []).filter((r) => !q || r.name.toLowerCase().includes(q) || (r.description ?? '').toLowerCase().includes(q));
  }, [data, query]);

  const remove = async (r: Recipe) => {
    if (!window.confirm(`Delete the recipe '${r.name}'? This cannot be undone.`)) return;
    setProblem('');
    try { await recipes.delete(r.name); refetch(); } catch (e) {
      const msg = e instanceof Error ? e.message : 'Delete failed';
      // The server keeps a recipe's runs and refuses to delete a recipe that has any.
      setProblem(/recipe_runs|foreign key/i.test(msg) ? `"${r.name}" has runs, so it cannot be deleted yet. Edit it instead.` : msg);
    }
  };

  if (loading && !data) return <Spinner />;
  if (error && !data) return <ErrorBanner message={error} onRetry={refetch} />;

  return (
    <div>
      <PageHeader title="Recipes" description="Workflows that chain agents, checks and people. Each row shows its steps in order, so you can see what a recipe does without opening it."
        action={<Button onClick={() => navigate('/recipes/new')}><Plus size={16} className="mr-1.5" /> New recipe</Button>} />
      <div className="px-4 sm:px-8 pb-8 space-y-5">
        {problem && <ErrorBanner message={problem} />}
        {waiting > 0 && (
          <Link to="/dishshelf" className="flex items-center gap-3 px-4 py-3 rounded-xl bg-[var(--ao-brand)]/10 border border-[var(--ao-brand)]/35 text-sm">
            <UserCheck size={18} className="text-[var(--ao-brand-light)]" />
            <span className="flex-1">{waiting === 1 ? '1 run is' : `${waiting} runs are`} waiting for someone to approve a step.</span>
            <span className="font-semibold text-[var(--ao-brand-light)]">Open DishShelf</span>
          </Link>
        )}
        {(data ?? []).length === 0 ? (
          <EmptyState icon={<BookOpen size={48} />} title="No recipes yet" description="A recipe chains agents, checks and people into one workflow." action={canWrite ? <Button onClick={() => navigate('/recipes/new')}>New recipe</Button> : undefined} />
        ) : (
          <>
            <label className="relative block max-w-[380px]">
              <span className="sr-only">Search recipes</span>
              <Search size={16} className="absolute left-3 top-1/2 -translate-y-1/2 text-[var(--ao-text-muted)] pointer-events-none" />
              <input type="text" value={query} onChange={(e) => setQuery(e.target.value)} placeholder="Search recipes"
                className="w-full h-10 pl-9 pr-3 rounded-[10px] border border-[var(--ao-border)] bg-[var(--ao-surface)] text-sm outline-none focus:border-[var(--ao-brand-light)]" />
            </label>
            <div className="rounded-2xl border border-[var(--ao-border)] bg-[var(--ao-surface)] overflow-x-auto">
              <div className="min-w-[900px]">
                <div className="grid gap-4 px-5 h-[42px] items-center border-b border-[var(--ao-border)] text-xs font-semibold uppercase tracking-wider text-[var(--ao-text-muted)]" style={{ gridTemplateColumns: COLS }}>
                  <span>Recipe</span><span>What it does, in order</span><span>Version</span><span>Last run</span><span />
                </div>
                {shown.map((r) => {
                  const last = lastRun(runs[r.name]);
                  return (
                    <div key={r.id} className="grid gap-4 px-5 py-4 items-center border-b border-[var(--ao-border)] last:border-b-0" style={{ gridTemplateColumns: COLS }}>
                      <div className="min-w-0">
                        <Link to={`/recipes/${encodeURIComponent(r.name)}`} className="block text-[15px] font-semibold truncate hover:text-[var(--ao-brand-light)]">{r.name}</Link>
                        <div className={`mt-0.5 text-[13px] truncate ${r.description ? 'text-[var(--ao-text-muted)]' : 'italic text-[var(--ao-text-muted)]/60'}`}>{r.description || 'No description'}</div>
                      </div>
                      <Flow steps={(r.steps ?? []) as WireStep[]} />
                      <div className="text-[13px] text-slate-300 font-mono">{r.version ? `v${r.version.replace(/^v/, '')}` : ''}</div>
                      <div className="flex items-center gap-2 text-[13px]" style={{ color: last.color }}><span className="w-2 h-2 rounded-full shrink-0" style={{ background: last.dot }} />{last.text}</div>
                      <div className="flex items-center justify-end gap-2">
                        <button type="button" onClick={() => setRunning(r.name)} disabled={(r.steps ?? []).length === 0}
                          title={(r.steps ?? []).length === 0 ? 'Add a step first' : undefined}
                          className="inline-flex items-center gap-1.5 h-[34px] px-3 rounded-lg text-[13px] font-semibold border border-[var(--ao-brand)]/55 text-[var(--ao-brand-light)] hover:bg-[var(--ao-brand)]/10 disabled:opacity-40 disabled:cursor-not-allowed">
                          <Play size={13} fill="currentColor" /> Run
                        </button>
                        <Link to={`/recipes/${encodeURIComponent(r.name)}`} className="inline-flex items-center h-[34px] px-3 rounded-lg text-[13px] font-medium border border-[var(--ao-border)] hover:bg-[var(--ao-surface-hover)]">Edit</Link>
                        {canWrite && <ActionDropdown items={[{ label: 'Delete', icon: <Trash2 size={14} />, onClick: () => remove(r), danger: true }]} label={r.name} />}
                      </div>
                    </div>
                  );
                })}
                {shown.length === 0 && <div className="px-5 py-8 text-sm text-center text-[var(--ao-text-muted)]">No recipe matches &ldquo;{query}&rdquo;.</div>}
              </div>
            </div>
            <div className="flex flex-wrap items-center gap-x-5 gap-y-2 text-[12.5px] text-[var(--ao-text-muted)]">
              <span className="text-[11.5px] font-semibold uppercase tracking-wider">Step types</span>
              {PALETTE.flatMap((g) => g.items).map((m) => {
                const Icon = m.icon;
                return <span key={m.type} className="inline-flex items-center gap-1.5"><span className="w-5 h-5 rounded-full grid place-items-center" style={{ background: m.bg, color: m.color }}><Icon size={11} /></span>{m.label}</span>;
              })}
            </div>
          </>
        )}
      </div>
      <RunRecipeModal recipe={running} onClose={() => setRunning(null)} onStarted={(n) => navigate(`/dishshelf/${encodeURIComponent(n)}`)} stepCount={(data ?? []).find((r) => r.name === running)?.steps?.length} />
    </div>
  );
}
