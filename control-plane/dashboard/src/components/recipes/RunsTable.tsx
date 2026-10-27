import { useMemo, useState } from 'react';
import { RotateCcw } from 'lucide-react';
import type { RecipeRun } from '../../api';
import { StatusBadge } from '../UI';

function fmtTime(iso?: string): string {
  return iso ? new Date(iso).toLocaleString([], { dateStyle: 'medium', timeStyle: 'short' }) : '';
}

function fmtDuration(ms?: number): string {
  if (!ms) return '—';
  if (ms < 1000) return `${ms} ms`;
  if (ms < 60_000) return `${(ms / 1000).toFixed(1)} s`;
  return `${(ms / 60_000).toFixed(1)} min`;
}

const FILTERS: { id: string; label: string; match: (r: RecipeRun) => boolean }[] = [
  { id: 'all', label: 'All', match: () => true },
  { id: 'running', label: 'Running', match: (r) => r.status === 'running' || r.status === 'submitted' },
  { id: 'waiting', label: 'Waiting', match: (r) => r.status === 'paused' },
  { id: 'failed', label: 'Failed', match: (r) => r.status === 'failed' },
  { id: 'completed', label: 'Completed', match: (r) => r.status === 'completed' },
];

/** The runs of one recipe, newest first: open one, or run it again with the same input. */
export default function RunsTable({ runs, archived, rerunning, onOpen, onRerun }: {
  runs: RecipeRun[];
  /** An archived recipe's runs are kept to read, but it can no longer be run. */
  archived?: boolean;
  rerunning?: string | null;
  onOpen: (run: RecipeRun) => void;
  onRerun: (run: RecipeRun) => void;
}) {
  const [filter, setFilter] = useState('all');
  const [query, setQuery] = useState('');
  const shown = useMemo(() => {
    const f = FILTERS.find((x) => x.id === filter) ?? FILTERS[0];
    const q = query.trim().toLowerCase();
    return runs
      .filter((r) => f.match(r) && (!q || r.id.toLowerCase().includes(q) || JSON.stringify(r.input ?? {}).toLowerCase().includes(q)))
      .sort((a, b) => (b.started_at ?? '').localeCompare(a.started_at ?? ''));
  }, [runs, filter, query]);

  const cols = 'grid-cols-[110px_110px_1fr_90px_80px_90px_150px]';
  return (
    <div className="px-4 sm:px-8 pb-8">
      <div className="flex flex-wrap items-center gap-2 mb-3">
        <div role="group" aria-label="Status" className="flex rounded-[10px] border border-[var(--ao-border)] overflow-hidden">
          {FILTERS.map((f) => (
            <button key={f.id} type="button" aria-pressed={filter === f.id} onClick={() => setFilter(f.id)}
              className={`h-[34px] px-3 text-[13px] ${filter === f.id ? 'bg-[var(--ao-surface-hover)] text-[var(--ao-text)] font-medium' : 'text-[var(--ao-text-muted)] hover:text-[var(--ao-text)]'}`}>
              {f.label} <span className="text-[11px] opacity-60">{runs.filter(f.match).length}</span>
            </button>
          ))}
        </div>
        <input value={query} onChange={(e) => setQuery(e.target.value)} placeholder="Search run ID or input"
          className="h-[34px] w-64 px-3 rounded-[10px] border border-[var(--ao-border)] bg-[var(--ao-surface)] text-sm outline-none focus:border-[var(--ao-brand-light)]" />
      </div>

      {runs.length === 0 ? (
        <p className="text-sm text-[var(--ao-text-muted)]">This recipe has not been run yet.</p>
      ) : (
        <div className="rounded-2xl border border-[var(--ao-border)] bg-[var(--ao-surface)] overflow-x-auto">
          <div className="min-w-[860px]">
            <div className={`grid ${cols} gap-3 px-5 h-[40px] items-center border-b border-[var(--ao-border)] text-xs font-semibold uppercase tracking-wider text-[var(--ao-text-muted)]`}>
              <span>Run</span><span>Status</span><span>Input</span><span>Duration</span><span>Tokens</span><span>Cost</span><span />
            </div>
            {shown.map((r) => {
              const input = r.input ? (typeof r.input.text === 'string' ? r.input.text : JSON.stringify(r.input)) : '';
              return (
                <div key={r.id} onClick={() => onOpen(r)}
                  className={`grid ${cols} gap-3 px-5 py-3 items-center border-b border-[var(--ao-border)] last:border-b-0 cursor-pointer hover:bg-[var(--ao-surface-hover)]`}>
                  <div>
                    <div className="font-mono text-[12.5px]">{r.id.slice(0, 8)}</div>
                    <div className="text-[11.5px] text-[var(--ao-text-muted)]">{fmtTime(r.started_at)}</div>
                  </div>
                  <div><StatusBadge status={r.status} />{r.rerun_of && <div className="text-[11px] text-[var(--ao-text-muted)] mt-0.5" title={`Run again of ${r.rerun_of}`}>again of {r.rerun_of.slice(0, 6)}</div>}</div>
                  <div className="text-[13px] text-[var(--ao-text-muted)] truncate" title={input}>{input || '—'}</div>
                  <div className="text-[13px]">{fmtDuration(r.duration_ms)}</div>
                  <div className="text-[13px]">{r.total_tokens ? r.total_tokens.toLocaleString() : '—'}</div>
                  <div className="text-[13px] font-mono">{r.total_cost_usd ? `$${r.total_cost_usd.toFixed(4)}` : '—'}</div>
                  <div className="flex justify-end">
                    <button type="button" disabled={archived || rerunning === r.id}
                      title={archived ? 'This recipe is archived: restore it to run it' : 'Run again with the same input'}
                      onClick={(e) => { e.stopPropagation(); onRerun(r); }}
                      className="inline-flex items-center gap-1.5 h-[30px] px-2.5 rounded-lg text-[12.5px] font-medium border border-[var(--ao-border)] hover:bg-[var(--ao-surface)] disabled:opacity-40 disabled:cursor-not-allowed">
                      <RotateCcw size={12} className={rerunning === r.id ? 'animate-spin' : ''} /> Run again
                    </button>
                  </div>
                </div>
              );
            })}
            {shown.length === 0 && <div className="px-5 py-8 text-sm text-center text-[var(--ao-text-muted)]">No run matches.</div>}
          </div>
        </div>
      )}
    </div>
  );
}
