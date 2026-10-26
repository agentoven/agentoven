import { useEffect, useMemo, useState } from 'react';
import { Link } from 'react-router-dom';
import { useAPI } from '../hooks';
import { recipeRuns, recipes, type RecipeRun } from '../api';
import { EmptyState, ErrorBanner, PageHeader, Spinner } from '../components/UI';
import { Layers } from 'lucide-react';

type ShelfId = 'waiting' | 'running' | 'finished' | 'problem';
const SHELVES: { id: ShelfId; title: string; dot: string; badge: string }[] = [
  { id: 'waiting', title: 'Waiting for you', dot: '#fbbf24', badge: 'bg-[var(--ao-brand)]/20 text-[var(--ao-brand-light)]' },
  { id: 'running', title: 'Running', dot: '#60a5fa', badge: 'bg-[var(--ao-bg)] text-[var(--ao-text-muted)]' },
  { id: 'finished', title: 'Finished', dot: '#4ade80', badge: 'bg-[var(--ao-bg)] text-[var(--ao-text-muted)]' },
  { id: 'problem', title: 'Needs a look', dot: '#f87171', badge: 'bg-red-400/15 text-red-300' },
];
const RANGES = [{ id: 1, label: 'Today' }, { id: 7, label: '7 days' }, { id: 30, label: '30 days' }];

interface Run extends RecipeRun { recipe: string }

function shelfOf(r: RecipeRun): ShelfId {
  if (r.status === 'paused') return 'waiting';
  if (r.status === 'failed') return 'problem';
  if (r.status === 'completed' || r.status === 'canceled') return 'finished';
  return 'running';
}

function ago(iso?: string): string {
  if (!iso) return '';
  const s = Math.max(0, (Date.now() - new Date(iso).getTime()) / 1000);
  if (s < 60) return 'Just now';
  if (s < 3600) return `${Math.floor(s / 60)} minutes ago`;
  if (s < 86400) return `${Math.floor(s / 3600)} hours ago`;
  return `${Math.floor(s / 86400)} days ago`;
}

/** Where a run stands, in a sentence. */
function note(r: Run): { text: string; color: string } {
  const steps = r.step_results ?? [];
  const done = steps.filter((s) => s.status === 'completed' || s.status === 'skipped' || s.status === 'success').length;
  if (r.status === 'paused') {
    const gate = steps.find((s) => s.gate_status === 'waiting');
    return { text: gate ? `Waiting at "${gate.step_name}"` : 'Waiting for approval', color: '#fde68a' };
  }
  if (r.status === 'failed') {
    const bad = steps.find((s) => s.status === 'failed');
    return { text: bad ? `Stopped at "${bad.step_name}"${bad.error ? `: ${bad.error}` : ''}` : r.error || 'Stopped with an error', color: '#fca5a5' };
  }
  if (r.status === 'completed') return { text: `${done} step${done === 1 ? '' : 's'} done${r.duration_ms ? ` in ${(r.duration_ms / 1000).toFixed(1)} s` : ''}`, color: '#94a3b8' };
  if (r.status === 'canceled') return { text: 'Canceled', color: '#94a3b8' };
  return { text: `${done} step${done === 1 ? '' : 's'} done so far`, color: '#cbd5e1' };
}

const barColor = (status: string): string =>
  status === 'completed' || status === 'success' ? '#4ade80' : status === 'skipped' ? '#475569' : status === 'failed' ? '#f87171'
    : status === 'running' ? '#60a5fa' : status === 'waiting' || status === 'paused' ? '#fbbf24' : '#334155';

/** Every run of every recipe, shelved by where it stands. What needs a person comes first. */
export default function DishShelf() {
  const list = useAPI(() => recipes.list());
  const [all, setAll] = useState<Run[]>([]);
  const [loadingRuns, setLoadingRuns] = useState(true);
  const [recipe, setRecipe] = useState('');
  const [days, setDays] = useState(7);

  
  const names = (list.data ?? []).map((r) => r.name).join('|');
  useEffect(() => {
    if (!list.data) return;
    let live = true;
    const ns = names ? names.split('|') : [];
    Promise.all(ns.map(async (n) => (await recipeRuns.list(n).catch(() => [] as RecipeRun[])).map((r) => ({ ...r, recipe: n }))))
      .then((groups) => { if (live) { setAll(groups.flat()); setLoadingRuns(false); } });
    return () => { live = false; };
  }, [names, list.data]);

  const [now] = useState(() => Date.now());
  const shelves = useMemo(() => {
    const since = now - days * 86400_000;
    const runs = all.filter((r) => (!recipe || r.recipe === recipe) && (r.status === 'paused' || r.status === 'running' || r.status === 'submitted' || new Date(r.started_at).getTime() >= since));
    const by: Record<ShelfId, Run[]> = { waiting: [], running: [], finished: [], problem: [] };
    for (const r of runs.sort((a, b) => (b.started_at ?? '').localeCompare(a.started_at ?? ''))) by[shelfOf(r)].push(r);
    return by;
  }, [all, recipe, days, now]);

  const total = Object.values(shelves).reduce((n, l) => n + l.length, 0);

  return (
    <div>
      <PageHeader title="DishShelf" description="Every run of every recipe, shelved by where it stands. What needs a person is always first." action={<>
        <label className="sr-only" htmlFor="shelf-recipe">Recipe</label>
        <select id="shelf-recipe" value={recipe} onChange={(e) => setRecipe(e.target.value)} className="h-10 w-48 px-3 rounded-[10px] bg-[var(--ao-surface)] border border-[var(--ao-border)] text-sm outline-none">
          <option value="">All recipes</option>
          {(list.data ?? []).map((r) => <option key={r.id} value={r.name}>{r.name}</option>)}
        </select>
        <div role="group" aria-label="Time" className="flex rounded-[10px] border border-[var(--ao-border)] overflow-hidden">
          {RANGES.map((r) => (
            <button key={r.id} type="button" aria-pressed={days === r.id} onClick={() => setDays(r.id)}
              className={`h-[38px] px-3.5 text-[13px] ${days === r.id ? 'bg-[var(--ao-surface-hover)] text-[var(--ao-text)] font-medium' : 'text-[var(--ao-text-muted)] hover:text-[var(--ao-text)]'}`}>{r.label}</button>
          ))}
        </div>
      </>} />
      <div className="px-4 sm:px-8 pb-8 space-y-4">
        {list.error && <ErrorBanner message={list.error} onRetry={list.refetch} />}
        {list.loading || loadingRuns ? <Spinner /> : (list.data ?? []).length === 0 ? (
          <EmptyState icon={<Layers size={44} />} title="No recipes yet" description="Runs of your recipes appear here." action={<Link to="/recipes/new" className="text-[var(--ao-brand-light)] font-semibold hover:underline">Create a recipe</Link>} />
        ) : (
          <>
            {total === 0 && <p className="text-sm text-[var(--ao-text-muted)] m-0">No runs in this time. Try a longer range, or start one from Recipes.</p>}
            <div className="grid gap-4 grid-cols-1 sm:grid-cols-2 xl:grid-cols-4 items-start">
              {SHELVES.map((s) => (
                <section key={s.id} aria-label={s.title} className="rounded-2xl border border-[var(--ao-border)] bg-[var(--ao-surface)]/55 p-3.5 flex flex-col gap-2.5 min-h-[240px]">
                  <div className="flex items-center gap-2 px-1 pb-1.5">
                    <span className="w-2 h-2 rounded-full" style={{ background: s.dot }} />
                    <h2 className="text-sm font-semibold m-0">{s.title}</h2>
                    <span className={`ml-auto min-w-[22px] h-5 px-1.5 rounded-full inline-flex items-center justify-center text-xs font-semibold ${s.badge}`}>{shelves[s.id].length}</span>
                  </div>
                  {shelves[s.id].map((r) => {
                    const n = note(r);
                    const bars = (r.step_results ?? []).map((x) => barColor(x.status));
                    return (
                      <Link key={r.id} to={`/dishshelf/${encodeURIComponent(r.recipe)}/runs/${r.id}`}
                        className={`block p-3.5 rounded-xl bg-[var(--ao-surface)] border ${s.id === 'waiting' ? 'border-amber-400/50' : s.id === 'problem' ? 'border-red-400/40' : 'border-[var(--ao-border)]'} hover:border-[var(--ao-brand-light)]`}>
                        <div className="flex items-baseline justify-between gap-2">
                          <span className="text-[14.5px] font-semibold truncate">{r.recipe}</span>
                          <span className="font-mono text-[11.5px] text-[var(--ao-text-muted)]">{r.id.slice(0, 4)}</span>
                        </div>
                        <div className="mt-1.5 text-[13px] [overflow-wrap:anywhere]" style={{ color: n.color }}>{n.text}</div>
                        {bars.length > 0 && <div className="flex gap-1 mt-3" aria-hidden="true">{bars.map((b, i) => <span key={i} className="flex-1 h-[5px] rounded-sm" style={{ background: b }} />)}</div>}
                        <div className="flex items-center justify-between mt-2.5 text-xs text-[var(--ao-text-muted)]">
                          <span>{ago(r.started_at)}</span>
                          <span className="font-mono">{r.total_cost_usd ? `$${r.total_cost_usd.toFixed(3)}` : ''}</span>
                        </div>
                        {s.id === 'waiting' && <span className="mt-3 h-[34px] grid place-items-center rounded-lg bg-[var(--ao-brand)] text-[#0f172a] text-[13px] font-semibold">Review and approve</span>}
                      </Link>
                    );
                  })}
                  {shelves[s.id].length === 0 && <p className="text-[13px] text-[var(--ao-text-muted)] m-0 px-1">{s.id === 'waiting' ? 'Nothing is waiting for you.' : 'Nothing here.'}</p>}
                </section>
              ))}
            </div>
          </>
        )}
      </div>
    </div>
  );
}
