import { useState, useMemo, useEffect } from 'react';
import { useParams, useNavigate, Link } from 'react-router-dom';
import { Download, GitCompare, ChevronRight, ChevronDown } from 'lucide-react';
import { useAPI } from '../hooks';
import { traces, type Trace, type Span, type SpanKind, type ThinkingBlock } from '../api';
import {
  PageHeader, StatusBadge, Spinner, ErrorBanner, Card,
} from '../components/UI';
import { SpanIO, ThinkingPanel, CopyButton, formatJSON } from '../components/traces/SpanIO';
import { TagEditor, FeedbackPanel } from '../components/traces/TraceFeedback';

// ── Helpers ──────────────────────────────────────────────────

function formatDuration(ms: number): string {
  if (ms < 1) return '<1ms';
  if (ms < 1000) return `${Math.round(ms)}ms`;
  if (ms < 60_000) return `${(ms / 1000).toFixed(2)}s`;
  return `${(ms / 60_000).toFixed(1)}m`;
}

function formatTokens(n: number): string {
  if (!n) return '0';
  if (n >= 1_000_000) return `${(n / 1_000_000).toFixed(1)}M`;
  if (n >= 1000) return `${(n / 1000).toFixed(1)}K`;
  return String(n);
}

// ── Span Kind Styling ────────────────────────────────────────

const kindColors: Record<SpanKind, { bar: string; badge: string; icon: string; label: string }> = {
  agent:     { bar: 'bg-blue-500',    badge: 'bg-blue-500/20 text-blue-400',    icon: '🤖', label: 'Agent' },
  llm:       { bar: 'bg-violet-500',  badge: 'bg-violet-500/20 text-violet-400', icon: '🧠', label: 'LLM' },
  tool:      { bar: 'bg-amber-500',   badge: 'bg-amber-500/20 text-amber-400',   icon: '🔧', label: 'Tool' },
  retriever: { bar: 'bg-cyan-500',    badge: 'bg-cyan-500/20 text-cyan-400',     icon: '🔍', label: 'Retriever' },
  chain:     { bar: 'bg-emerald-500', badge: 'bg-emerald-500/20 text-emerald-400', icon: '🔗', label: 'Chain' },
  embedding: { bar: 'bg-pink-500',    badge: 'bg-pink-500/20 text-pink-400',     icon: '📐', label: 'Embedding' },
};

function SpanKindBadge({ kind }: { kind: SpanKind }) {
  const cfg = kindColors[kind] || kindColors.chain;
  return (
    <span className={`inline-flex items-center gap-1 px-2 py-0.5 rounded text-[0.7rem] font-semibold ${cfg.badge}`}>
      {cfg.icon} {cfg.label}
    </span>
  );
}

// ── Tree ─────────────────────────────────────────────────────

interface SpanNode { span: Span; children: SpanNode[]; depth: number; }

function buildSpanTree(spans: Span[]): SpanNode[] {
  const map = new Map<string, SpanNode>();
  const roots: SpanNode[] = [];
  for (const s of spans) map.set(s.id, { span: s, children: [], depth: 0 });
  for (const s of spans) {
    const node = map.get(s.id)!;
    if (s.parent_span_id && map.has(s.parent_span_id)) {
      map.get(s.parent_span_id)!.children.push(node);
    } else {
      roots.push(node);
    }
  }
  function setDepth(ns: SpanNode[], d: number) { for (const n of ns) { n.depth = d; setDepth(n.children, d + 1); } }
  setDepth(roots, 0);
  return roots;
}

function flattenTree(nodes: SpanNode[]): SpanNode[] {
  const r: SpanNode[] = [];
  function walk(ns: SpanNode[]) { for (const n of ns) { r.push(n); walk(n.children); } }
  walk(nodes);
  return r;
}

// ── Main Page ────────────────────────────────────────────────

function formatCost(c: number): string {
  if (!c) return '$0';
  if (c < 0.0001) return `$${c.toExponential(1)}`;
  return `$${c.toFixed(c < 0.01 ? 5 : 4)}`;
}

export function TraceDetailPage() {
  const { traceId } = useParams<{ traceId: string }>();
  const navigate = useNavigate();
  const { data, loading, error, refetch } = useAPI(() => traces.get(traceId!));
  // The trace as shown: the loaded one, plus the tags and feedback changed on this page.
  const [edited, setEdited] = useState<Trace | null>(null);
  const trace = edited && edited.id === data?.id ? edited : data;
  const [selectedSpan, setSelectedSpan] = useState<Span | null>(null);
  const [activeTab, setActiveTab] = useState<'trace' | 'waterfall' | 'spans' | 'metadata'>('trace');
  const [children, setChildren] = useState<Trace[]>([]);

  useEffect(() => { setEdited(null); setSelectedSpan(null); }, [traceId]);
  useEffect(() => {
    if (!traceId) return;
    traces.list({ parent: traceId, sort: 'created_at', order: 'asc', limit: 100, include_archived: true }).then(setChildren).catch(() => setChildren([]));
  }, [traceId]);

  if (loading) return <Spinner />;
  if (error) return <ErrorBanner message={error} onRetry={refetch} />;
  if (!trace) return <ErrorBanner message="Trace not found" />;

  const runId = typeof trace.metadata?.run_id === 'string' ? trace.metadata.run_id : '';
  // Back returns to the list as it was left (filters, page), when the page was opened from it.
  const back = () => ((window.history.state?.idx ?? 0) > 0 ? navigate(-1) : navigate('/traces'));
  const download = () => {
    const blob = new Blob([JSON.stringify(trace, null, 2)], { type: 'application/json' });
    const a = document.createElement('a');
    a.href = URL.createObjectURL(blob);
    a.download = `trace-${trace.id}.json`;
    a.click();
    URL.revokeObjectURL(a.href);
  };

  return (
    <div>
      <PageHeader
        title={
          <div className="flex items-center gap-3">
            <button onClick={back} title="Back to traces" className="text-[var(--ao-text-muted)] text-xl hover:text-[var(--ao-text)]">←</button>
            <span>{trace.agent_name}</span>
            <StatusBadge status={trace.status} />
          </div> as unknown as string
        }
        description={`Trace ${trace.id.slice(0, 12)}… • ${new Date(trace.created_at).toLocaleString()}`}
        action={
          <div className="flex items-center gap-2">
            <button onClick={() => navigate(`/traces/compare?a=${trace.id}`)} className="flex items-center gap-1.5 px-3 py-1.5 text-sm rounded-md border border-[var(--ao-border)] hover:bg-[var(--ao-surface-hover)]">
              <GitCompare size={14} /> Compare
            </button>
            <button onClick={download} className="flex items-center gap-1.5 px-3 py-1.5 text-sm rounded-md border border-[var(--ao-border)] hover:bg-[var(--ao-surface-hover)]">
              <Download size={14} /> JSON
            </button>
          </div>
        }
      />

      {/* Where this trace sits */}
      <div className="px-8 pt-3 flex flex-wrap items-center gap-x-4 gap-y-2 text-sm">
        {trace.recipe_name && (
          <span className="text-[var(--ao-text-muted)]">
            Recipe{' '}
            {runId
              ? <Link className="text-[var(--ao-brand-light)] hover:underline" to={`/recipes/${encodeURIComponent(trace.recipe_name)}/runs/${runId}`}>{trace.recipe_name}</Link>
              : trace.recipe_name}
            {typeof trace.metadata?.step_name === 'string' && <> · step <span className="text-[var(--ao-text)]">{trace.metadata.step_name}</span></>}
          </span>
        )}
        {trace.parent_trace_id && <Link className="text-[var(--ao-brand-light)] hover:underline" to={`/traces/${trace.parent_trace_id}`}>↑ Parent trace</Link>}
        {trace.session_id && <Link className="text-[var(--ao-brand-light)] hover:underline" to={`/traces?session=${encodeURIComponent(trace.session_id)}`}>Session {trace.session_id.slice(0, 12)}</Link>}
        {typeof trace.metadata?.model === 'string' && <span className="text-[var(--ao-text-muted)]">Model <span className="text-[var(--ao-text)]">{trace.metadata.model}</span></span>}
        <div className="ml-auto"><TagEditor trace={trace} onChange={setEdited} /></div>
      </div>

      {/* Stats */}
      <div className="px-8 pt-3 grid grid-cols-2 md:grid-cols-6 gap-3 mb-4">
        <Card><div className="text-[0.7rem] text-[var(--ao-text-muted)] uppercase tracking-wider">Duration</div><div className="text-xl font-bold mt-0.5">{formatDuration(trace.duration_ms)}</div></Card>
        <Card><div className="text-[0.7rem] text-[var(--ao-text-muted)] uppercase tracking-wider">Total Tokens</div><div className="text-xl font-bold mt-0.5">{formatTokens(trace.total_tokens || 0)}</div></Card>
        <Card><div className="text-[0.7rem] text-[var(--ao-text-muted)] uppercase tracking-wider">Cost</div><div className="text-xl font-bold mt-0.5">{formatCost(trace.cost_usd ?? 0)}</div></Card>
        <Card><div className="text-[0.7rem] text-[var(--ao-text-muted)] uppercase tracking-wider">Spans</div><div className="text-xl font-bold mt-0.5">{trace.spans?.length || 0}</div></Card>
        <Card><div className="text-[0.7rem] text-[var(--ao-text-muted)] uppercase tracking-wider">In / Out Tokens</div><div className="text-xl font-bold mt-0.5">{trace.usage ? `${formatTokens(trace.usage.input_tokens)} / ${formatTokens(trace.usage.output_tokens)}` : '—'}</div></Card>
        <Card><div className="text-[0.7rem] text-[var(--ao-text-muted)] uppercase tracking-wider">Thinking</div><div className="text-xl font-bold mt-0.5">{trace.usage?.thinking_tokens ? formatTokens(trace.usage.thinking_tokens) : trace.thinking_blocks?.length ? `${trace.thinking_blocks.length} blocks` : '—'}</div></Card>
      </div>

      {/* Input/Output */}
      {(trace.input_text || trace.output_text) && (
        <div className="px-8 grid grid-cols-1 md:grid-cols-2 gap-3 mb-4">
          {trace.input_text && (
            <Card>
              <div className="flex items-center justify-between mb-2"><span className="text-[0.7rem] text-[var(--ao-text-muted)] uppercase tracking-wider">Input</span><CopyButton text={trace.input_text} /></div>
              <pre className="text-sm whitespace-pre-wrap break-words max-h-32 overflow-auto">{trace.input_text}</pre>
            </Card>
          )}
          {trace.output_text && (
            <Card>
              <div className="flex items-center justify-between mb-2"><span className="text-[0.7rem] text-[var(--ao-text-muted)] uppercase tracking-wider">Output</span><CopyButton text={trace.output_text} /></div>
              <pre className="text-sm whitespace-pre-wrap break-words max-h-32 overflow-auto">{trace.output_text}</pre>
            </Card>
          )}
        </div>
      )}

      <div className="px-8 mb-4"><FeedbackPanel trace={trace} onChange={setEdited} /></div>

      {children.length > 0 && (
        <div className="px-8 mb-4">
          <Card>
            <div className="text-[0.7rem] text-[var(--ao-text-muted)] uppercase tracking-wider mb-2">Child traces ({children.length})</div>
            <div className="flex flex-col">
              {children.map((c) => (
                <Link key={c.id} to={`/traces/${c.id}`} className="flex items-center justify-between py-1.5 border-t border-[var(--ao-border)] text-sm hover:text-[var(--ao-brand-light)]">
                  <span className="flex items-center gap-2"><StatusBadge status={c.status} />{c.agent_name}{typeof c.metadata?.step_name === 'string' && <span className="text-xs text-[var(--ao-text-muted)]">· {c.metadata.step_name}</span>}</span>
                  <span className="text-xs text-[var(--ao-text-muted)]">{formatDuration(c.duration_ms)} · {formatTokens(c.total_tokens)} tok · {formatCost(c.cost_usd ?? 0)}</span>
                </Link>
              ))}
            </div>
          </Card>
        </div>
      )}

      {/* Tabs */}
      <div className="px-8 flex gap-1 mb-4 border-b border-[var(--ao-border)]">
        {(['trace', 'waterfall', 'spans', 'metadata'] as const).map((tab) => (
          <button
            key={tab}
            onClick={() => setActiveTab(tab)}
            className={`px-4 py-2 text-sm font-medium capitalize ${activeTab === tab ? 'text-[var(--ao-brand)] border-b-2 border-[var(--ao-brand)]' : 'text-[var(--ao-text-muted)] hover:text-[var(--ao-text)]'}`}
          >
            {tab}
          </button>
        ))}
      </div>

      <div className="px-8 pb-8">
        {activeTab === 'trace' && <TraceSplitView trace={trace} />}
        {activeTab === 'waterfall' && <WaterfallView trace={trace} selectedSpan={selectedSpan} onSelectSpan={setSelectedSpan} />}
        {activeTab === 'spans' && <SpanListView trace={trace} onSelectSpan={setSelectedSpan} />}
        {activeTab === 'metadata' && <MetadataView trace={trace} />}
      </div>

      {/* Span Detail Slide-Over (waterfall and list) */}
      {selectedSpan && activeTab !== 'trace' && <SpanDetailPanel span={selectedSpan} onClose={() => setSelectedSpan(null)} />}
    </div>
  );
}

// ── Trace view: the span tree beside the selected span ───────

function TraceSplitView({ trace }: { trace: Trace }) {
  const spans = trace.spans || [];
  const tree = useMemo(() => buildSpanTree(spans), [spans]);
  const [selected, setSelected] = useState<string | null>(null);
  const [collapsed, setCollapsed] = useState<Set<string>>(new Set());
  const [kinds, setKinds] = useState<Set<SpanKind>>(new Set());
  const [query, setQuery] = useState('');

  const flat = useMemo(() => {
    const out: SpanNode[] = [];
    const walk = (ns: SpanNode[]) => ns.forEach((n) => { out.push(n); if (!collapsed.has(n.span.id)) walk(n.children); });
    walk(tree);
    return out;
  }, [tree, collapsed]);

  const byModel = useMemo(() => {
    const m = new Map<string, { calls: number; tokens: number; cost: number; ms: number }>();
    for (const sp of spans) {
      if (sp.kind !== 'llm') continue;
      const key = `${sp.provider ? sp.provider + '/' : ''}${sp.model || sp.name}`;
      const cur = m.get(key) ?? { calls: 0, tokens: 0, cost: 0, ms: 0 };
      cur.calls++; cur.tokens += sp.usage?.total_tokens ?? 0; cur.cost += sp.usage?.estimated_cost_usd ?? 0; cur.ms += sp.duration_ms;
      m.set(key, cur);
    }
    return [...m.entries()];
  }, [spans]);

  if (!spans.length) return <WaterfallView trace={trace} selectedSpan={null} onSelectSpan={() => undefined} />;

  const q = query.trim().toLowerCase();
  const visible = flat.filter((n) =>
    (kinds.size === 0 || kinds.has(n.span.kind)) &&
    (!q || n.span.name.toLowerCase().includes(q) || (n.span.model ?? '').toLowerCase().includes(q) || JSON.stringify(n.span.output ?? '').toLowerCase().includes(q)));
  const span = spans.find((s) => s.id === selected) ?? visible[0]?.span ?? spans[0];
  const kindsPresent = [...new Set(spans.map((s) => s.kind))];
  const toggle = (id: string) => setCollapsed((c) => { const n = new Set(c); if (n.has(id)) n.delete(id); else n.add(id); return n; });
  const totalMs = Math.max(trace.duration_ms, 1);

  return (
    <div>
      {byModel.length > 0 && (
        <Card className="mb-3 !p-3">
          <div className="text-[0.7rem] text-[var(--ao-text-muted)] uppercase tracking-wider mb-1.5">Model calls</div>
          <div className="grid grid-cols-[1fr_60px_80px_80px_80px] gap-x-3 text-xs">
            <span className="text-[var(--ao-text-muted)]">Model</span><span className="text-[var(--ao-text-muted)]">Calls</span><span className="text-[var(--ao-text-muted)]">Tokens</span><span className="text-[var(--ao-text-muted)]">Time</span><span className="text-[var(--ao-text-muted)]">Cost</span>
            {byModel.map(([name, v]) => (
              <div key={name} className="contents">
                <span className="font-medium truncate">{name}</span><span>{v.calls}</span><span>{formatTokens(v.tokens)}</span><span>{formatDuration(v.ms)}</span><span>{formatCost(v.cost)}</span>
              </div>
            ))}
          </div>
        </Card>
      )}

      <div className="flex flex-wrap items-center gap-2 mb-2">
        <input value={query} onChange={(e) => setQuery(e.target.value)} placeholder="Search spans"
          className="px-2.5 py-1.5 text-sm rounded-md border border-[var(--ao-border)] bg-[var(--ao-surface)] w-56" />
        {kindsPresent.map((k) => (
          <button key={k} onClick={() => setKinds((c) => { const n = new Set(c); if (n.has(k)) n.delete(k); else n.add(k); return n; })}
            className={`px-2 py-1 rounded text-xs border ${kinds.has(k) ? 'border-[var(--ao-brand)] text-[var(--ao-text)]' : 'border-[var(--ao-border)] text-[var(--ao-text-muted)]'}`}>
            {(kindColors[k] || kindColors.chain).icon} {(kindColors[k] || kindColors.chain).label}
          </button>
        ))}
        <button onClick={() => setCollapsed(new Set(spans.filter((s) => spans.some((c) => c.parent_span_id === s.id)).map((s) => s.id)))} className="text-xs text-[var(--ao-text-muted)] hover:text-[var(--ao-text)] ml-auto">Collapse all</button>
        <button onClick={() => setCollapsed(new Set())} className="text-xs text-[var(--ao-text-muted)] hover:text-[var(--ao-text)]">Expand all</button>
      </div>

      <div className="grid grid-cols-1 lg:grid-cols-[minmax(320px,2fr)_3fr] gap-3 items-start">
        {/* Tree */}
        <Card className="!p-2 max-h-[640px] overflow-auto">
          {visible.map((n) => {
            const sp = n.span;
            const kcfg = kindColors[sp.kind] || kindColors.chain;
            const failed = sp.status === 'failed' || sp.status === 'error' || !!sp.error;
            const hasKids = n.children.length > 0;
            return (
              <div key={sp.id} onClick={() => setSelected(sp.id)}
                className={`flex items-center gap-1.5 py-1.5 pr-2 rounded cursor-pointer ${span?.id === sp.id ? 'bg-[var(--ao-surface-hover)]' : 'hover:bg-white/5'}`}
                style={{ paddingLeft: 6 + n.depth * 16 }}>
                <button onClick={(e) => { e.stopPropagation(); if (hasKids) toggle(sp.id); }} className={`w-4 text-[var(--ao-text-muted)] ${hasKids ? '' : 'invisible'}`}>
                  {collapsed.has(sp.id) ? <ChevronRight size={13} /> : <ChevronDown size={13} />}
                </button>
                <span title={kcfg.label}>{kcfg.icon}</span>
                <div className="min-w-0 flex-1">
                  <div className={`text-[0.8rem] font-medium truncate ${failed ? 'text-red-400' : ''}`}>{sp.name}</div>
                  <div className="text-[0.65rem] text-[var(--ao-text-muted)] truncate">
                    {sp.model ? `${sp.model} · ` : ''}{formatDuration(sp.duration_ms)}
                    {sp.usage?.total_tokens ? ` · ${formatTokens(sp.usage.total_tokens)} tok` : ''}
                    {sp.usage?.estimated_cost_usd ? ` · ${formatCost(sp.usage.estimated_cost_usd)}` : ''}
                  </div>
                  <div className="h-1 mt-1 rounded bg-[var(--ao-border)] overflow-hidden"><div className={`h-full ${kcfg.bar}`} style={{ width: `${Math.min(100, (sp.duration_ms / totalMs) * 100)}%` }} /></div>
                </div>
              </div>
            );
          })}
          {visible.length === 0 && <div className="p-4 text-sm text-[var(--ao-text-muted)]">No span matches.</div>}
        </Card>

        {/* Selected span */}
        {span && <SpanDetail span={span} />}
      </div>
    </div>
  );
}

function SpanDetail({ span }: { span: Span }) {
  const [tab, setTab] = useState<'input' | 'output' | 'details'>('output');
  const kcfg = kindColors[span.kind] || kindColors.chain;
  const hasInput = span.input != null;
  const t = tab === 'input' && !hasInput ? 'output' : tab;
  return (
    <Card className="!p-0 overflow-hidden">
      <div className="p-3 border-b border-[var(--ao-border)]">
        <div className="flex items-center gap-2 flex-wrap">
          <span className="text-lg">{kcfg.icon}</span>
          <span className="font-bold">{span.name}</span>
          <SpanKindBadge kind={span.kind} />
          <StatusBadge status={span.status} />
          {span.model && <span className="text-xs px-2 py-0.5 rounded bg-[var(--ao-bg)] text-[var(--ao-text-muted)]">{span.model}</span>}
          {span.provider && <span className="text-xs px-2 py-0.5 rounded bg-[var(--ao-bg)] text-[var(--ao-text-muted)]">{span.provider}</span>}
        </div>
        <div className="flex gap-4 mt-2 text-xs text-[var(--ao-text-muted)] flex-wrap">
          <span>{formatDuration(span.duration_ms)}</span>
          {span.usage && <span>{formatTokens(span.usage.input_tokens)} in · {formatTokens(span.usage.output_tokens)} out{span.usage.thinking_tokens ? ` · ${formatTokens(span.usage.thinking_tokens)} thinking` : ''}</span>}
          {!!span.usage?.estimated_cost_usd && <span>{formatCost(span.usage.estimated_cost_usd)}{span.usage.price_source === 'fallback' ? ' (placeholder price)' : ''}</span>}
          {!!span.usage?.cached_tokens && <span>{formatTokens(span.usage.cached_tokens)} cached</span>}
        </div>
        {span.error && <pre className="text-xs whitespace-pre-wrap mt-2 p-2 bg-red-500/10 border border-red-500/30 rounded text-red-300">{span.error}</pre>}
      </div>
      <div className="px-3 flex gap-1 border-b border-[var(--ao-border)]">
        {(['input', 'output', 'details'] as const).filter((x) => x !== 'input' || hasInput).map((x) => (
          <button key={x} onClick={() => setTab(x)}
            className={`px-3 py-2 text-sm font-medium capitalize ${t === x ? 'text-[var(--ao-brand)] border-b-2 border-[var(--ao-brand)]' : 'text-[var(--ao-text-muted)] hover:text-[var(--ao-text)]'}`}>{x}</button>
        ))}
      </div>
      <div className="p-3 max-h-[560px] overflow-auto">
        {t === 'input' && <SpanIO value={span.input} empty="No input recorded" />}
        {t === 'output' && <SpanIO value={span.output} empty="No output recorded" />}
        {t === 'details' && (
          <div className="text-sm">
            {span.usage?.price_source && <InfoRow label="Price source" value={span.usage.price_source.replace('_', ' ')} />}
            {!!span.usage?.cache_savings_usd && <InfoRow label="Saved by caching" value={formatCost(span.usage.cache_savings_usd)} />}
            <InfoRow label="Start" value={new Date(span.start_time).toISOString()} mono />
            <InfoRow label="End" value={new Date(span.end_time).toISOString()} mono />
            <InfoRow label="Span ID" value={span.id} mono />
            {span.parent_span_id && <InfoRow label="Parent" value={span.parent_span_id} mono />}
            {span.events && span.events.length > 0 && (
              <>
                <div className="text-[0.7rem] text-[var(--ao-text-muted)] uppercase tracking-wider font-semibold mt-4 mb-1">Events</div>
                {span.events.map((e, i) => (
                  <div key={i} className="py-1 border-b border-[var(--ao-border)]">
                    <span className="font-semibold">{e.name}</span>
                    {e.attributes && <pre className="text-xs whitespace-pre-wrap break-words text-[var(--ao-text-muted)]">{formatJSON(e.attributes)}</pre>}
                  </div>
                ))}
              </>
            )}
            {span.metadata && Object.keys(span.metadata).length > 0 && (
              <>
                <div className="text-[0.7rem] text-[var(--ao-text-muted)] uppercase tracking-wider font-semibold mt-4 mb-1">Metadata</div>
                <pre className="text-xs whitespace-pre-wrap break-words p-2 bg-[var(--ao-bg)] rounded border border-[var(--ao-border)]">{formatJSON(span.metadata)}</pre>
              </>
            )}
          </div>
        )}
      </div>
    </Card>
  );
}

// ── Waterfall ────────────────────────────────────────────────

function WaterfallView({ trace, selectedSpan, onSelectSpan }: { trace: Trace; selectedSpan: Span | null; onSelectSpan: (s: Span) => void }) {
  const spans = trace.spans || [];

  const { flatNodes, traceStart, traceDuration } = useMemo(() => {
    const tree = buildSpanTree(spans);
    const flat = flattenTree(tree);
    let start = Infinity, end = -Infinity;
    for (const s of spans) {
      const st = new Date(s.start_time).getTime();
      const et = new Date(s.end_time).getTime();
      if (st < start) start = st;
      if (et > end) end = et;
    }
    return { flatNodes: flat, traceStart: start, traceDuration: Math.max(end - start, 1) };
  }, [spans]);

  if (!spans.length) {
    return (
      <Card className="text-center py-8">
        <div className="text-3xl mb-2">📊</div>
        <div className="font-semibold">No spans recorded</div>
        <div className="text-sm text-[var(--ao-text-muted)] mt-1">This trace has no hierarchical span data</div>
      </Card>
    );
  }

  const marks = [0, 0.25, 0.5, 0.75, 1.0];

  return (
    <Card className="!p-4 overflow-x-auto">
      {/* Ruler */}
      <div className="flex relative h-6 mb-2 border-b border-[var(--ao-border)]" style={{ marginLeft: 280 }}>
        {marks.map((pct) => (
          <div key={pct} className="absolute text-[0.65rem] text-[var(--ao-text-muted)] -translate-x-1/2" style={{ left: `${pct * 100}%` }}>
            {formatDuration(pct * traceDuration)}
          </div>
        ))}
      </div>

      {/* Rows */}
      {flatNodes.map((node) => {
        const startMs = new Date(node.span.start_time).getTime() - traceStart;
        const leftPct = (startMs / traceDuration) * 100;
        const widthPct = Math.max((node.span.duration_ms / traceDuration) * 100, 0.5);
        const kcfg = kindColors[node.span.kind] || kindColors.chain;
        const isSelected = selectedSpan?.id === node.span.id;

        return (
          <div
            key={node.span.id}
            onClick={() => onSelectSpan(node.span)}
            className={`flex items-center h-9 cursor-pointer rounded ${isSelected ? 'bg-[var(--ao-surface-hover)]' : 'hover:bg-white/5'}`}
          >
            {/* Label */}
            <div className="w-[280px] shrink-0 flex items-center gap-1.5 overflow-hidden" style={{ paddingLeft: node.depth * 20 + 8 }}>
              {node.depth > 0 && <span className="text-[var(--ao-border)] text-[0.7rem] mr-0.5">└</span>}
              <SpanKindBadge kind={node.span.kind} />
              <span className="text-[0.8rem] font-medium truncate">{node.span.name}</span>
            </div>
            {/* Timeline */}
            <div className="flex-1 relative h-full">
              {marks.map((pct) => (
                <div key={pct} className="absolute top-0 bottom-0 w-px bg-[var(--ao-border)] opacity-30" style={{ left: `${pct * 100}%` }} />
              ))}
              <div
                className={`absolute top-2 h-5 rounded ${kcfg.bar} ${node.span.status === 'error' ? 'ring-1 ring-red-500' : ''}`}
                style={{ left: `${leftPct}%`, width: `${widthPct}%`, minWidth: 4, opacity: 0.85 }}
              />
              <div className="absolute top-2.5 text-[0.65rem] font-semibold whitespace-nowrap" style={{ left: `calc(${leftPct + widthPct}% + 6px)` }}>
                {formatDuration(node.span.duration_ms)}
                {node.span.usage?.total_tokens ? ` • ${formatTokens(node.span.usage.total_tokens)} tok` : ''}
              </div>
            </div>
          </div>
        );
      })}
    </Card>
  );
}

// ── Span List ────────────────────────────────────────────────

function SpanListView({ trace, onSelectSpan }: { trace: Trace; onSelectSpan: (s: Span) => void }) {
  const spans = trace.spans || [];
  if (!spans.length) return <Card className="text-center py-8 text-[var(--ao-text-muted)]">No spans</Card>;

  return (
    <div className="flex flex-col gap-1.5">
      {spans.map((span) => (
        <div
          key={span.id}
          onClick={() => onSelectSpan(span)}
          className="flex items-center justify-between bg-[var(--ao-surface)] border border-[var(--ao-border)] rounded-lg px-4 py-2.5 cursor-pointer hover:border-[var(--ao-brand)] transition-colors"
        >
          <div className="flex items-center gap-2">
            <SpanKindBadge kind={span.kind} />
            <span className="font-semibold text-sm">{span.name}</span>
            {span.model && <span className="text-xs text-[var(--ao-text-muted)]">({span.model})</span>}
            <StatusBadge status={span.status} />
          </div>
          <div className="flex gap-4 text-xs text-[var(--ao-text-muted)]">
            <span>{formatDuration(span.duration_ms)}</span>
            {span.usage && <span>{formatTokens(span.usage.total_tokens)} tokens</span>}
            <span className="font-mono text-[0.7rem]">{span.id.slice(0, 8)}</span>
          </div>
        </div>
      ))}
    </div>
  );
}

// ── Metadata ─────────────────────────────────────────────────

function MetadataView({ trace }: { trace: Trace }) {
  return (
    <div className="grid grid-cols-2 gap-3">
      <Card>
        <div className="text-[0.7rem] text-[var(--ao-text-muted)] uppercase tracking-wider mb-3">Trace Info</div>
        <InfoRow label="ID" value={trace.id} mono />
        <InfoRow label="Agent" value={trace.agent_name} />
        {trace.recipe_name && <InfoRow label="Recipe" value={trace.recipe_name} />}
        <InfoRow label="Kitchen" value={trace.kitchen} />
        <InfoRow label="Status" value={trace.status} />
        {trace.session_id && <InfoRow label="Session ID" value={trace.session_id} mono />}
        {trace.parent_trace_id && <InfoRow label="Parent Trace" value={trace.parent_trace_id} mono />}
        <InfoRow label="Created" value={new Date(trace.created_at).toLocaleString()} />
      </Card>
      <Card>
        <div className="text-[0.7rem] text-[var(--ao-text-muted)] uppercase tracking-wider mb-3">Usage</div>
        <InfoRow label="Total Tokens" value={String(trace.total_tokens || 0)} />
        <InfoRow label="Cost" value={`$${(trace.cost_usd ?? 0).toFixed(6)}`} />
        <InfoRow label="Duration" value={formatDuration(trace.duration_ms)} />
        {trace.usage && <>
          <InfoRow label="Input Tokens" value={String(trace.usage.input_tokens)} />
          <InfoRow label="Output Tokens" value={String(trace.usage.output_tokens)} />
          {!!trace.usage.thinking_tokens && <InfoRow label="Thinking Tokens" value={String(trace.usage.thinking_tokens)} />}
          {!!trace.usage.cached_tokens && <InfoRow label="Cached Tokens" value={String(trace.usage.cached_tokens)} />}
        </>}
        {trace.tags && trace.tags.length > 0 && <InfoRow label="Tags" value={trace.tags.join(', ')} />}
      </Card>
      {trace.thinking_blocks && trace.thinking_blocks.length > 0 && (
        <Card>
          <ThinkingPanel blocks={trace.thinking_blocks} tokens={trace.usage?.thinking_tokens} />
        </Card>
      )}
      {trace.metadata && Object.keys(trace.metadata).length > 0 && (
        <Card>
          <div className="text-[0.7rem] text-[var(--ao-text-muted)] uppercase tracking-wider mb-3">Metadata</div>
          <pre className="text-sm whitespace-pre-wrap break-words max-h-72 overflow-auto">{formatJSON(trace.metadata)}</pre>
        </Card>
      )}
    </div>
  );
}

// ── Thinking ─────────────────────────────────────────────────

/** The reasoning an LLM span recorded: it travels in the span's output next to the answer. */
function thinkingOf(span: Span): ThinkingBlock[] {
  const out = span.output as { thinking_blocks?: ThinkingBlock[] } | undefined;
  return Array.isArray(out?.thinking_blocks) ? out!.thinking_blocks! : [];
}

// ── Span Detail Panel ────────────────────────────────────────

function SpanDetailPanel({ span, onClose }: { span: Span; onClose: () => void }) {
  const [tab, setTab] = useState<'overview' | 'input' | 'output'>('overview');
  const kcfg = kindColors[span.kind] || kindColors.chain;

  return (
    <div className="fixed top-0 right-0 bottom-0 w-[520px] bg-[var(--ao-surface)] border-l border-[var(--ao-border)] z-50 flex flex-col shadow-2xl">
      {/* Header */}
      <div className="p-4 border-b border-[var(--ao-border)] shrink-0">
        <div className="flex justify-between items-center">
          <div className="flex items-center gap-2">
            <span className="text-xl">{kcfg.icon}</span>
            <span className="font-bold">{span.name}</span>
          </div>
          <button onClick={onClose} className="text-[var(--ao-text-muted)] text-xl hover:text-[var(--ao-text)]">✕</button>
        </div>
        <div className="flex gap-2 mt-2 items-center">
          <SpanKindBadge kind={span.kind} />
          <StatusBadge status={span.status} />
          {span.model && <span className="text-xs text-[var(--ao-text-muted)] bg-[var(--ao-bg)] px-2 py-0.5 rounded">{span.model}</span>}
          {span.provider && <span className="text-xs text-[var(--ao-text-muted)] bg-[var(--ao-bg)] px-2 py-0.5 rounded">{span.provider}</span>}
        </div>
      </div>

      {/* Tabs */}
      <div className="px-4 flex gap-1 border-b border-[var(--ao-border)] shrink-0">
        {(['overview', 'input', 'output'] as const).map((t) => (
          <button
            key={t}
            onClick={() => setTab(t)}
            className={`px-3 py-2 text-sm font-medium capitalize ${tab === t ? 'text-[var(--ao-brand)] border-b-2 border-[var(--ao-brand)]' : 'text-[var(--ao-text-muted)] hover:text-[var(--ao-text)]'}`}
          >
            {t}
          </button>
        ))}
      </div>

      {/* Content */}
      <div className="flex-1 overflow-auto p-4">
        {tab === 'overview' && (
          <>
            {/* Metrics */}
            <div className="grid grid-cols-2 gap-2 mb-4">
              <div className="bg-[var(--ao-bg)] rounded-md p-3"><div className="text-[0.65rem] text-[var(--ao-text-muted)] uppercase tracking-wider">Duration</div><div className="text-base font-bold mt-0.5">{formatDuration(span.duration_ms)}</div></div>
              <div className="bg-[var(--ao-bg)] rounded-md p-3"><div className="text-[0.65rem] text-[var(--ao-text-muted)] uppercase tracking-wider">Total Tokens</div><div className="text-base font-bold mt-0.5">{span.usage ? formatTokens(span.usage.total_tokens) : '—'}</div></div>
              <div className="bg-[var(--ao-bg)] rounded-md p-3"><div className="text-[0.65rem] text-[var(--ao-text-muted)] uppercase tracking-wider">Input Tokens</div><div className="text-base font-bold mt-0.5">{span.usage ? String(span.usage.input_tokens) : '—'}</div></div>
              <div className="bg-[var(--ao-bg)] rounded-md p-3"><div className="text-[0.65rem] text-[var(--ao-text-muted)] uppercase tracking-wider">Output Tokens</div><div className="text-base font-bold mt-0.5">{span.usage ? String(span.usage.output_tokens) : '—'}</div></div>
              {span.usage?.thinking_tokens ? <div className="bg-[var(--ao-bg)] rounded-md p-3"><div className="text-[0.65rem] text-[var(--ao-text-muted)] uppercase tracking-wider">Thinking</div><div className="text-base font-bold mt-0.5">{String(span.usage.thinking_tokens)}</div></div> : null}
              {span.usage?.estimated_cost_usd ? <div className="bg-[var(--ao-bg)] rounded-md p-3"><div className="text-[0.65rem] text-[var(--ao-text-muted)] uppercase tracking-wider">Cost</div><div className="text-base font-bold mt-0.5">${span.usage.estimated_cost_usd.toFixed(6)}</div></div> : null}
            </div>

            {span.usage?.price_source === 'fallback' && (
              <div className="text-xs p-2 mb-2 rounded-md border border-amber-500/30 bg-amber-500/10 text-amber-300">
                No price is known for this model, so the cost shown is a placeholder. Set one with <code>model_costs</code> on the provider.
              </div>
            )}
            {!!span.usage?.cached_tokens && (
              <InfoRow label="Cached prompt tokens" value={`${span.usage.cached_tokens}${span.usage.cache_savings_usd ? ` (saved $${span.usage.cache_savings_usd.toFixed(6)})` : ''}`} />
            )}
            {!!span.usage?.cache_creation && <InfoRow label="Cache write tokens" value={String(span.usage.cache_creation)} />}
            {span.usage?.price_source && <InfoRow label="Price source" value={span.usage.price_source.replace('_', ' ')} />}

            <ThinkingPanel blocks={thinkingOf(span)} tokens={span.usage?.thinking_tokens} />

            {/* Timing */}
            <div className="text-[0.7rem] text-[var(--ao-text-muted)] uppercase tracking-wider font-semibold mt-5 mb-2">Timing</div>
            <InfoRow label="Start" value={new Date(span.start_time).toISOString()} mono />
            <InfoRow label="End" value={new Date(span.end_time).toISOString()} mono />
            <InfoRow label="Duration" value={formatDuration(span.duration_ms)} />

            {/* IDs */}
            <div className="text-[0.7rem] text-[var(--ao-text-muted)] uppercase tracking-wider font-semibold mt-5 mb-2">Identifiers</div>
            <InfoRow label="Span ID" value={span.id} mono />
            <InfoRow label="Trace ID" value={span.trace_id} mono />
            {span.parent_span_id && <InfoRow label="Parent Span" value={span.parent_span_id} mono />}

            {/* Error */}
            {span.error && (
              <>
                <div className="text-[0.7rem] text-[var(--ao-text-muted)] uppercase tracking-wider font-semibold mt-5 mb-2">Error</div>
                <pre className="text-sm whitespace-pre-wrap p-3 bg-red-500/10 border border-red-500/30 rounded-md text-red-300">{span.error}</pre>
              </>
            )}

            {/* Events */}
            {span.events && span.events.length > 0 && (
              <>
                <div className="text-[0.7rem] text-[var(--ao-text-muted)] uppercase tracking-wider font-semibold mt-5 mb-2">Events ({span.events.length})</div>
                {span.events.map((evt, i) => (
                  <div key={i} className="py-1.5 border-b border-[var(--ao-border)] text-sm">
                    <span className="font-semibold">{evt.name}</span>
                    <span className="text-[var(--ao-text-muted)] ml-2 text-[0.7rem]">{new Date(evt.timestamp).toISOString()}</span>
                    {evt.attributes && Object.keys(evt.attributes).length > 0 && (
                      <pre className="text-xs whitespace-pre-wrap break-words mt-1 text-[var(--ao-text-muted)]">{formatJSON(evt.attributes)}</pre>
                    )}
                  </div>
                ))}
              </>
            )}

            {/* Metadata */}
            {span.metadata && Object.keys(span.metadata).length > 0 && (
              <>
                <div className="text-[0.7rem] text-[var(--ao-text-muted)] uppercase tracking-wider font-semibold mt-5 mb-2">Metadata</div>
                <pre className="text-sm whitespace-pre-wrap break-words p-3 bg-[var(--ao-bg)] rounded-md border border-[var(--ao-border)] max-h-[500px] overflow-auto">{formatJSON(span.metadata)}</pre>
              </>
            )}
          </>
        )}

        {tab === 'input' && <SpanIO value={span.input} empty="No input data" />}
        {tab === 'output' && <SpanIO value={span.output} empty="No output data" />}
      </div>
    </div>
  );
}

// ── Shared ───────────────────────────────────────────────────

function InfoRow({ label, value, mono }: { label: string; value: string; mono?: boolean }) {
  return (
    <div className="flex justify-between py-1 border-b border-[var(--ao-border)] text-sm">
      <span className="text-[var(--ao-text-muted)]">{label}</span>
      <span className={`text-right max-w-[60%] break-all ${mono ? 'font-mono text-xs' : ''}`}>{value}</span>
    </div>
  );
}
