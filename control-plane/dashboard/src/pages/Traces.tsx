import { useState, useEffect, useCallback, useMemo } from 'react';
import { useNavigate, useSearchParams } from 'react-router-dom';
import { Activity, RefreshCw, Search, Download, GitCompare, ArrowDown, ArrowUp, X, ChevronDown, ChevronRight } from 'lucide-react';
import { traces, type Trace, type TraceFilters, type TraceStats, type TraceSession } from '../api';
import {
  PageHeader, Card, StatusBadge, EmptyState,
  Spinner, ErrorBanner,
} from '../components/UI';

export function formatDuration(ms: number): string {
  if (ms < 1) return '<1ms';
  if (ms < 1000) return `${Math.round(ms)}ms`;
  if (ms < 60_000) return `${(ms / 1000).toFixed(2)}s`;
  return `${(ms / 60_000).toFixed(1)}m`;
}

export function formatTokens(n: number): string {
  if (!n) return '0';
  if (n >= 1_000_000) return `${(n / 1_000_000).toFixed(1)}M`;
  if (n >= 1000) return `${(n / 1000).toFixed(1)}K`;
  return String(n);
}

export function formatCost(c: number): string {
  if (!c) return '$0';
  if (c < 0.0001) return `$${c.toExponential(1)}`;
  return `$${c.toFixed(c < 0.01 ? 5 : 4)}`;
}

function timeAgo(dateStr: string): string {
  const diff = Date.now() - new Date(dateStr).getTime();
  const secs = Math.floor(diff / 1000);
  if (secs < 60) return `${secs}s ago`;
  const mins = Math.floor(secs / 60);
  if (mins < 60) return `${mins}m ago`;
  const hrs = Math.floor(mins / 60);
  if (hrs < 24) return `${hrs}h ago`;
  return `${Math.floor(hrs / 24)}d ago`;
}

const PAGE = 50;

/** The model a trace used: what its own metadata says, else the models of its spans (the list carries no spans). */
export function modelOf(t: Trace): string {
  const m = t.metadata?.model;
  return typeof m === 'string' ? m : '';
}

const selectCls = 'px-2.5 py-1.5 text-sm rounded-md border border-[var(--ao-border)] bg-[var(--ao-surface)]';

type SortKey = NonNullable<TraceFilters['sort']>;

const GRID = 'grid-cols-[28px_1fr_90px_150px_80px_70px_90px_70px]';

/** One trace as a row. A recipe run (a recipe's own trace) opens to show its steps when the list is grouped. */
function TraceRow({ trace, picked, onPick, onOpen, onTag, expander, indent }: {
  trace: Trace; picked: string[]; onPick: (id: string) => void; onOpen: (id: string) => void; onTag: (tag: string) => void;
  expander?: { open: boolean; toggle: () => void; steps?: number };
  indent?: boolean;
}) {
  const realtime = !!(trace.tags?.includes('realtime') || trace.metadata?.source === 'realtime');
  const recipe = !!trace.recipe_name;
  return (
    <div
      onClick={() => onOpen(trace.id)}
      className={`grid ${GRID} gap-3 items-center border border-[var(--ao-border)] rounded-lg px-4 py-3 cursor-pointer hover:border-[var(--ao-brand)] transition-colors ${indent ? 'bg-[var(--ao-bg)] opacity-90' : 'bg-[var(--ao-surface)]'}`}
      style={{ borderLeft: `3px solid ${realtime ? '#10b981' : recipe ? '#8b5cf6' : '#3b82f6'}` }}
    >
      <input type="checkbox" checked={picked.includes(trace.id)} title="Select to compare"
        onClick={(e) => e.stopPropagation()} onChange={() => onPick(trace.id)} />
      <div className="min-w-0">
        <div className="flex items-center gap-2">
          {expander && (
            <button type="button" aria-label={expander.open ? 'Hide steps' : 'Show steps'} title={expander.open ? 'Hide steps' : 'Show steps'}
              onClick={(e) => { e.stopPropagation(); expander.toggle(); }} className="text-[var(--ao-text-muted)] hover:text-[var(--ao-text)] -ml-1">
              {expander.open ? <ChevronDown size={16} /> : <ChevronRight size={16} />}
            </button>
          )}
          <span className="font-semibold text-sm truncate">{expander ? (trace.recipe_name || trace.agent_name) : trace.agent_name}</span>
          {expander && <span className="text-[0.65rem] px-1.5 py-0.5 rounded bg-purple-500/10 text-purple-300 border border-purple-500/20">📋 recipe run{expander.steps ? ` · ${expander.steps} ${expander.steps === 1 ? 'step' : 'steps'}` : ''}</span>}
          {realtime && <span className="text-[0.65rem] px-1.5 py-0.5 rounded bg-emerald-500/10 text-emerald-400 border border-emerald-500/20">🎙️ realtime</span>}
          {recipe && !expander && !indent && <span className="text-xs text-[var(--ao-text-muted)] truncate">/ {trace.recipe_name}</span>}
          {indent && typeof trace.metadata?.step_name === 'string' && <span className="text-xs text-[var(--ao-text-muted)] truncate">· step {trace.metadata.step_name}</span>}
          {(trace.tags ?? []).filter((tag) => tag !== 'realtime').slice(0, 3).map((tag) => (
            <span key={tag} onClick={(e) => { e.stopPropagation(); onTag(tag); }}
              className="text-[0.65rem] px-1.5 py-0.5 rounded bg-[var(--ao-bg)] text-[var(--ao-text-muted)] hover:text-[var(--ao-text)]">{tag}</span>
          ))}
        </div>
        {trace.input_text && <div className="text-xs text-[var(--ao-text-muted)] truncate mt-0.5">{trace.input_text.slice(0, 140)}</div>}
      </div>
      <StatusBadge status={trace.status} />
      <span className="text-xs text-[var(--ao-text-muted)] truncate" title={modelOf(trace)}>{modelOf(trace) || '—'}</span>
      <span className="text-sm font-medium">{formatDuration(trace.duration_ms)}</span>
      <span className="text-sm font-medium">{formatTokens(trace.total_tokens)}</span>
      <span className="text-sm font-medium">{formatCost(trace.cost_usd ?? 0)}</span>
      <span className="text-xs text-[var(--ao-text-muted)]">{timeAgo(trace.created_at)}</span>
    </div>
  );
}

/** A row of the list: an agent call as it is, a recipe run (when grouped) with its steps under it. */
function TraceItem({ trace, grouped, picked, onPick, onOpen, onTag, includeArchived }: {
  trace: Trace; grouped: boolean; picked: string[]; onPick: (id: string) => void; onOpen: (id: string) => void; onTag: (tag: string) => void;
  includeArchived: boolean;
}) {
  const isRun = grouped && !!trace.recipe_name && !trace.parent_trace_id;
  const [open, setOpen] = useState(false);
  const [steps, setSteps] = useState<Trace[] | null>(null);
  const [failed, setFailed] = useState(false);

  useEffect(() => {
    if (!isRun || !open || steps) return;
    traces.list({ parent: trace.id, sort: 'created_at', order: 'asc', limit: 200, include_archived: includeArchived })
      .then(setSteps).catch(() => setFailed(true));
  }, [isRun, open, steps, trace.id, includeArchived]);

  if (!isRun) return <TraceRow trace={trace} picked={picked} onPick={onPick} onOpen={onOpen} onTag={onTag} />;
  return (
    <div className="flex flex-col gap-1">
      <TraceRow trace={trace} picked={picked} onPick={onPick} onOpen={onOpen} onTag={onTag}
        expander={{ open, toggle: () => setOpen((o) => !o), steps: typeof trace.metadata?.steps === 'number' ? trace.metadata.steps : steps?.length }} />
      {open && (
        <div className="ml-6 pl-3 border-l-2 border-purple-500/25 flex flex-col gap-1">
          {!steps && !failed && <div className="text-xs text-[var(--ao-text-muted)] px-2 py-1">Loading steps…</div>}
          {failed && <div className="text-xs text-red-400 px-2 py-1">The steps could not be loaded.</div>}
          {steps && steps.length === 0 && <div className="text-xs text-[var(--ao-text-muted)] px-2 py-1">This run made no model calls (its steps were not agents).</div>}
          {steps?.map((st) => <TraceRow key={st.id} trace={st} picked={picked} onPick={onPick} onOpen={onOpen} onTag={onTag} indent />)}
        </div>
      )}
    </div>
  );
}

export function TracesPage() {
  const navigate = useNavigate();
  const [params, setParams] = useSearchParams();
  const [view, setView] = useState<'traces' | 'sessions'>('traces');

  // The filters live in the URL, so a filtered list can be bookmarked, shared, and returned to with Back.
  const filters: TraceFilters = useMemo(() => ({
    agent: params.get('agent') || undefined,
    status: params.get('status') || undefined,
    model: params.get('model') || undefined,
    tag: params.get('tag') || undefined,
    session: params.get('session') || undefined,
    recipe: params.get('recipe') || undefined,
    q: params.get('q') || undefined,
    range: (params.get('range') as TraceFilters['range']) || undefined,
    min_cost: params.get('min_cost') ? Number(params.get('min_cost')) : undefined,
    include_archived: params.get('include_archived') === 'true' || undefined,
    sort: (params.get('sort') as SortKey) || undefined,
    order: (params.get('order') as 'asc' | 'desc') || undefined,
  }), [params]);
  const page = Number(params.get('page') || 0);

  // A recipe run is one row, with its steps under it (opened on demand). Grouping is off when the list is
  // flat on request, and while a search names something that lives inside a run (an agent, a model, a tag, a
  // session, words): then the steps themselves are what is being looked for.
  const flat = params.get('flat') === 'true';
  const hunting = !!(filters.agent || filters.model || filters.tag || filters.session || filters.q);
  const grouped = !flat && !hunting;
  const query: TraceFilters = useMemo(() => ({ ...filters, roots: grouped || undefined }), [filters, grouped]);

  const setFilter = (key: string, value: string | undefined) => {
    const next = new URLSearchParams(params);
    if (value) next.set(key, value); else next.delete(key);
    if (key !== 'page') next.delete('page');
    setParams(next, { replace: true });
  };

  const [items, setItems] = useState<Trace[]>([]);
  const [total, setTotal] = useState(0);
  const [stats, setStats] = useState<TraceStats | null>(null);
  const [sessions, setSessions] = useState<TraceSession[]>([]);
  const [facets, setFacets] = useState<{ agents: string[]; models: string[]; tags: string[]; recipes: string[] }>({ agents: [], models: [], tags: [], recipes: [] });
  const [loading, setLoading] = useState(true);
  const [error, setError] = useState<string | null>(null);
  const [picked, setPicked] = useState<string[]>([]);
  const [search, setSearch] = useState(filters.q ?? '');

  // Debounce the search box into the URL.
  useEffect(() => {
    const t = setTimeout(() => { if ((filters.q ?? '') !== search) setFilter('q', search.trim() || undefined); }, 350);
    return () => clearTimeout(t);
  }, [search]); // eslint-disable-line react-hooks/exhaustive-deps

  // What the filter dropdowns offer: the agents, models and tags of the most recent traces.
  useEffect(() => {
    traces.list({ limit: 500, include_archived: filters.include_archived }).then((all) => {
      const uniq = (xs: (string | undefined)[]) => [...new Set(xs.filter((x): x is string => !!x))].sort();
      setFacets({
        agents: uniq(all.map((t) => t.agent_name)),
        models: uniq(all.map(modelOf)),
        tags: uniq(all.flatMap((t) => t.tags ?? [])),
        recipes: uniq(all.map((t) => t.recipe_name)),
      });
    }).catch(() => undefined);
  }, [filters.include_archived]);

  const fetchData = useCallback(() => {
    setLoading(true);
    setError(null);
    const q = { ...query };
    Promise.all([
      view === 'traces' ? traces.page({ ...q, limit: PAGE, offset: page * PAGE }) : Promise.resolve(null),
      traces.stats(q),
      view === 'sessions' ? traces.sessions(q) : Promise.resolve(null),
    ])
      .then(([p, st, se]) => {
        if (p) { setItems(p.items); setTotal(p.total); }
        setStats(st);
        if (se) setSessions(se);
      })
      .catch((e) => setError(e.message))
      .finally(() => setLoading(false));
  }, [query, page, view]);

  useEffect(() => { fetchData(); }, [fetchData]);

  const sortKey: SortKey = filters.sort ?? 'created_at';
  const desc = filters.order !== 'asc';
  const sortBy = (key: SortKey) => {
    const next = new URLSearchParams(params);
    if (sortKey === key) next.set('order', desc ? 'asc' : 'desc'); else { next.set('sort', key); next.set('order', 'desc'); }
    next.delete('page');
    setParams(next, { replace: true });
  };
  const SortHead = ({ k, children }: { k: SortKey; children: string }) => (
    <button onClick={() => sortBy(k)} className="inline-flex items-center gap-1 hover:text-[var(--ao-text)]">
      {children}{sortKey === k && (desc ? <ArrowDown size={11} /> : <ArrowUp size={11} />)}
    </button>
  );

  const active = Object.entries(filters).filter(([k, v]) => v !== undefined && !['sort', 'order'].includes(k));
  const clearAll = () => { setSearch(''); setParams(new URLSearchParams(), { replace: true }); };

  const togglePick = (id: string) => setPicked((p) => (p.includes(id) ? p.filter((x) => x !== id) : p.length >= 2 ? [p[1], id] : [...p, id]));

  const exportJSON = () => {
    const blob = new Blob([JSON.stringify(items, null, 2)], { type: 'application/json' });
    const a = document.createElement('a');
    a.href = URL.createObjectURL(blob);
    a.download = `traces-${new Date().toISOString().slice(0, 19)}.json`;
    a.click();
    URL.revokeObjectURL(a.href);
  };

  const errRate = stats && stats.count ? (stats.errors / stats.count) * 100 : 0;

  return (
    <div>
      <PageHeader
        title="Traces"
        description="Every model call, tool call and run: inputs, outputs, cost, latency and feedback"
        action={
          <div className="flex items-center gap-2">
            {picked.length === 2 && (
              <button
                onClick={() => navigate(`/traces/compare?a=${picked[0]}&b=${picked[1]}`)}
                className="flex items-center gap-1.5 px-3 py-1.5 text-sm rounded-md bg-[var(--ao-brand)] text-white"
              >
                <GitCompare size={14} /> Compare 2
              </button>
            )}
            <button onClick={exportJSON} disabled={!items.length} title="Download this page as JSON"
              className="flex items-center gap-1.5 px-3 py-1.5 text-sm rounded-md border border-[var(--ao-border)] hover:bg-[var(--ao-surface-hover)] disabled:opacity-40">
              <Download size={14} /> Export
            </button>
            <button onClick={fetchData}
              className="flex items-center gap-1.5 px-3 py-1.5 text-sm rounded-md border border-[var(--ao-border)] hover:bg-[var(--ao-surface-hover)]">
              <RefreshCw size={14} className={loading ? 'animate-spin' : ''} /> Refresh
            </button>
          </div>
        }
      />

      {/* Summary of what the filters match */}
      <div className="px-8 pt-4 grid grid-cols-2 md:grid-cols-6 gap-3 mb-4">
        {[
          ['Traces', String(stats?.count ?? 0)],
          ['Error rate', `${errRate.toFixed(errRate < 10 ? 1 : 0)}%`],
          ['p50 latency', formatDuration(stats?.p50_ms ?? 0)],
          ['p95 latency', formatDuration(stats?.p95_ms ?? 0)],
          ['Tokens', formatTokens(stats?.total_tokens ?? 0)],
          ['Cost', formatCost(stats?.total_cost_usd ?? 0)],
        ].map(([label, value]) => (
          <Card key={label}>
            <div className="text-xs text-[var(--ao-text-muted)] uppercase tracking-wider">{label}</div>
            <div className="text-xl font-bold mt-0.5">{value}</div>
          </Card>
        ))}
      </div>

      {/* View tabs */}
      <div className="px-8 flex gap-1 mb-3 border-b border-[var(--ao-border)]">
        {(['traces', 'sessions'] as const).map((v) => (
          <button key={v} onClick={() => setView(v)}
            className={`px-4 py-2 text-sm font-medium capitalize ${view === v ? 'text-[var(--ao-brand)] border-b-2 border-[var(--ao-brand)]' : 'text-[var(--ao-text-muted)] hover:text-[var(--ao-text)]'}`}>
            {v}
          </button>
        ))}
      </div>

      {/* Filters */}
      <div className="px-8 flex flex-wrap items-center gap-2 mb-3">
        <div className="relative">
          <Search size={14} className="absolute left-2.5 top-2.5 text-[var(--ao-text-muted)]" />
          <input
            value={search}
            onChange={(e) => setSearch(e.target.value)}
            placeholder="Search input, output, agent, ID"
            className={`${selectCls} pl-8 w-72`}
          />
        </div>
        <select value={filters.status ?? ''} onChange={(e) => setFilter('status', e.target.value || undefined)} title="Status" className={selectCls}>
          <option value="">All statuses</option>
          <option value="completed">Completed</option>
          <option value="failed">Failed</option>
          <option value="error">Error</option>
          <option value="blocked">Blocked</option>
        </select>
        <select value={filters.agent ?? ''} onChange={(e) => setFilter('agent', e.target.value || undefined)} title="Agent" className={selectCls}>
          <option value="">All agents</option>
          {facets.agents.map((a) => <option key={a} value={a}>{a}</option>)}
        </select>
        {facets.recipes.length > 0 && (
          <select value={filters.recipe ?? ''} onChange={(e) => setFilter('recipe', e.target.value || undefined)} title="Recipe" className={selectCls}>
            <option value="">All recipes</option>
            {facets.recipes.map((a) => <option key={a} value={a}>{a}</option>)}
          </select>
        )}
        <select value={filters.model ?? ''} onChange={(e) => setFilter('model', e.target.value || undefined)} title="Model" className={selectCls}>
          <option value="">All models</option>
          {facets.models.map((a) => <option key={a} value={a}>{a}</option>)}
        </select>
        <select value={filters.tag ?? ''} onChange={(e) => setFilter('tag', e.target.value || undefined)} title="Tag" className={selectCls}>
          <option value="">All tags</option>
          {facets.tags.map((a) => <option key={a} value={a}>{a}</option>)}
        </select>
        <select value={filters.range ?? ''} onChange={(e) => setFilter('range', e.target.value || undefined)} title="Time range" className={selectCls}>
          <option value="">Any time</option>
          <option value="1h">Last hour</option>
          <option value="24h">Last 24 hours</option>
          <option value="7d">Last 7 days</option>
          <option value="30d">Last 30 days</option>
        </select>
        <select value={filters.min_cost ?? ''} onChange={(e) => setFilter('min_cost', e.target.value || undefined)} title="Minimum cost" className={selectCls}>
          <option value="">Any cost</option>
          <option value="0.001">≥ $0.001</option>
          <option value="0.01">≥ $0.01</option>
          <option value="0.1">≥ $0.10</option>
          <option value="1">≥ $1</option>
        </select>
        <label className="flex items-center gap-1.5 text-sm text-[var(--ao-text-muted)]"
          title={hunting ? 'Grouping is off while you search for an agent, model, tag, session or text: the steps themselves are listed' : 'Show each recipe run as one row, with its steps under it'}>
          <input type="checkbox" checked={grouped} disabled={hunting} onChange={(e) => setFilter('flat', e.target.checked ? undefined : 'true')} />
          Group recipe runs
        </label>
        <label className="flex items-center gap-1.5 text-sm text-[var(--ao-text-muted)]" title="Runs of archived recipes are kept for audit but hidden by default">
          <input type="checkbox" checked={!!filters.include_archived} onChange={(e) => setFilter('include_archived', e.target.checked ? 'true' : undefined)} />
          Include archived recipes
        </label>
        {active.length > 0 && (
          <button onClick={clearAll} className="flex items-center gap-1 text-xs text-[var(--ao-text-muted)] hover:text-[var(--ao-text)]">
            <X size={12} /> Clear filters
          </button>
        )}
      </div>

      {error && <ErrorBanner message={error} onRetry={fetchData} />}

      {loading && !items.length && !sessions.length ? (
        <Spinner />
      ) : view === 'sessions' ? (
        sessions.length === 0 ? (
          <EmptyState icon={<Activity size={48} />} title="No sessions" description="A session groups the traces of one conversation. Calls made with a session ID appear here." />
        ) : (
          <div className="px-8 pb-8 flex flex-col gap-2">
            {sessions.map((s) => (
              <div key={s.session_id}
                onClick={() => { setFilter('session', s.session_id); setView('traces'); }}
                className="flex items-center gap-4 bg-[var(--ao-surface)] border border-[var(--ao-border)] rounded-lg p-4 cursor-pointer hover:border-[var(--ao-brand)] transition-colors">
                <div className="flex-1 min-w-0">
                  <div className="font-mono text-sm truncate">{s.session_id}</div>
                  <div className="text-xs text-[var(--ao-text-muted)] mt-0.5">{s.agents.join(', ')}</div>
                </div>
                <div className="flex gap-6 text-xs text-[var(--ao-text-muted)] shrink-0">
                  <div className="text-center"><div className="font-bold text-[var(--ao-text)]">{s.traces}</div><div>traces</div></div>
                  <div className="text-center"><div className={`font-bold ${s.errors ? 'text-red-400' : 'text-[var(--ao-text)]'}`}>{s.errors}</div><div>errors</div></div>
                  <div className="text-center"><div className="font-bold text-[var(--ao-text)]">{formatTokens(s.total_tokens)}</div><div>tokens</div></div>
                  <div className="text-center"><div className="font-bold text-[var(--ao-text)]">{formatCost(s.total_cost_usd)}</div><div>cost</div></div>
                  <div className="w-16 text-right">{timeAgo(s.last_at)}</div>
                </div>
              </div>
            ))}
          </div>
        )
      ) : items.length > 0 ? (
        <div className="px-8 pb-8">
          <div className="grid grid-cols-[28px_1fr_90px_150px_80px_70px_90px_70px] gap-3 px-4 py-2 text-[0.7rem] uppercase tracking-wider text-[var(--ao-text-muted)]">
            <span />
            <span>Trace</span>
            <span>Status</span>
            <span>Model</span>
            <SortHead k="duration">Latency</SortHead>
            <SortHead k="tokens">Tokens</SortHead>
            <SortHead k="cost">Cost</SortHead>
            <SortHead k="created_at">When</SortHead>
          </div>
          <div className="flex flex-col gap-1.5">
            {items.map((trace) => (
              <TraceItem key={trace.id} trace={trace} grouped={grouped} picked={picked} onPick={togglePick}
                onOpen={(id) => navigate(`/traces/${id}`)} onTag={(tag) => setFilter('tag', tag)}
                includeArchived={!!filters.include_archived} />
            ))}
          </div>

          <div className="flex items-center justify-between mt-4 text-sm text-[var(--ao-text-muted)]">
            <span>{page * PAGE + 1}–{page * PAGE + items.length} of {total}</span>
            <div className="flex gap-2">
              <button disabled={page === 0} onClick={() => setFilter('page', page > 1 ? String(page - 1) : undefined)}
                className="px-3 py-1.5 rounded-md border border-[var(--ao-border)] disabled:opacity-40 hover:bg-[var(--ao-surface-hover)]">Previous</button>
              <button disabled={(page + 1) * PAGE >= total} onClick={() => { const n = new URLSearchParams(params); n.set('page', String(page + 1)); setParams(n, { replace: true }); }}
                className="px-3 py-1.5 rounded-md border border-[var(--ao-border)] disabled:opacity-40 hover:bg-[var(--ao-surface-hover)]">Next</button>
            </div>
          </div>
        </div>
      ) : (
        <EmptyState
          icon={<Activity size={48} />}
          title={active.length ? 'No traces match these filters' : 'No traces yet'}
          description={active.length ? 'Try widening the time range or clearing a filter.' : 'Traces will appear here when agents are invoked.'}
        />
      )}
    </div>
  );
}
