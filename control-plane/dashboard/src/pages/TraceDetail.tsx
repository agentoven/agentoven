import { useState, useMemo } from 'react';
import { useParams, useNavigate } from 'react-router-dom';
import { useAPI } from '../hooks';
import { traces, type Trace, type Span, type SpanKind } from '../api';
import {
  PageHeader, StatusBadge, Spinner, ErrorBanner, Card,
} from '../components/UI';

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

function formatJSON(value: unknown): string {
  if (value == null) return '';
  if (typeof value === 'string') return value;
  try { return JSON.stringify(value, null, 2); } catch { return String(value); }
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

export function TraceDetailPage() {
  const { traceId } = useParams<{ traceId: string }>();
  const navigate = useNavigate();
  const { data: trace, loading, error, refetch } = useAPI(() => traces.get(traceId!));
  const [selectedSpan, setSelectedSpan] = useState<Span | null>(null);
  const [activeTab, setActiveTab] = useState<'waterfall' | 'spans' | 'metadata'>('waterfall');

  if (loading) return <Spinner />;
  if (error) return <ErrorBanner message={error} onRetry={refetch} />;
  if (!trace) return <ErrorBanner message="Trace not found" />;

  return (
    <div>
      {/* Header */}
      <PageHeader
        title={
          <div className="flex items-center gap-3">
            <button onClick={() => navigate('/traces')} className="text-[var(--ao-text-muted)] text-xl hover:text-[var(--ao-text)]">←</button>
            <span>{trace.agent_name}</span>
            <StatusBadge status={trace.status} />
          </div> as unknown as string
        }
        description={`Trace ${trace.id.slice(0, 12)}… • ${new Date(trace.created_at).toLocaleString()}`}
      />

      {/* Stats */}
      <div className="px-8 pt-4 grid grid-cols-5 gap-3 mb-4">
        <Card><div className="text-[0.7rem] text-[var(--ao-text-muted)] uppercase tracking-wider">Duration</div><div className="text-xl font-bold mt-0.5">{formatDuration(trace.duration_ms)}</div></Card>
        <Card><div className="text-[0.7rem] text-[var(--ao-text-muted)] uppercase tracking-wider">Total Tokens</div><div className="text-xl font-bold mt-0.5">{formatTokens(trace.total_tokens || 0)}</div></Card>
        <Card><div className="text-[0.7rem] text-[var(--ao-text-muted)] uppercase tracking-wider">Cost</div><div className="text-xl font-bold mt-0.5">${(trace.cost_usd ?? 0).toFixed(4)}</div></Card>
        <Card><div className="text-[0.7rem] text-[var(--ao-text-muted)] uppercase tracking-wider">Spans</div><div className="text-xl font-bold mt-0.5">{trace.spans?.length || 0}</div></Card>
        <Card><div className="text-[0.7rem] text-[var(--ao-text-muted)] uppercase tracking-wider">I/O Tokens</div><div className="text-xl font-bold mt-0.5">{trace.usage ? `${formatTokens(trace.usage.input_tokens)} / ${formatTokens(trace.usage.output_tokens)}` : '—'}</div></Card>
      </div>

      {/* Input/Output */}
      {(trace.input_text || trace.output_text) && (
        <div className="px-8 grid grid-cols-2 gap-3 mb-4">
          {trace.input_text && (
            <Card>
              <div className="text-[0.7rem] text-[var(--ao-text-muted)] uppercase tracking-wider mb-2">Input</div>
              <pre className="text-sm whitespace-pre-wrap break-words max-h-28 overflow-auto">{trace.input_text}</pre>
            </Card>
          )}
          {trace.output_text && (
            <Card>
              <div className="text-[0.7rem] text-[var(--ao-text-muted)] uppercase tracking-wider mb-2">Output</div>
              <pre className="text-sm whitespace-pre-wrap break-words max-h-28 overflow-auto">{trace.output_text}</pre>
            </Card>
          )}
        </div>
      )}

      {/* Tabs */}
      <div className="px-8 flex gap-1 mb-4 border-b border-[var(--ao-border)]">
        {(['waterfall', 'spans', 'metadata'] as const).map((tab) => (
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
        {activeTab === 'waterfall' && <WaterfallView trace={trace} selectedSpan={selectedSpan} onSelectSpan={setSelectedSpan} />}
        {activeTab === 'spans' && <SpanListView trace={trace} onSelectSpan={setSelectedSpan} />}
        {activeTab === 'metadata' && <MetadataView trace={trace} />}
      </div>

      {/* Span Detail Slide-Over */}
      {selectedSpan && <SpanDetailPanel span={selectedSpan} onClose={() => setSelectedSpan(null)} />}
    </div>
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
      {trace.metadata && Object.keys(trace.metadata).length > 0 && (
        <Card>
          <div className="text-[0.7rem] text-[var(--ao-text-muted)] uppercase tracking-wider mb-3">Metadata</div>
          <pre className="text-sm whitespace-pre-wrap break-words max-h-72 overflow-auto">{formatJSON(trace.metadata)}</pre>
        </Card>
      )}
    </div>
  );
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

        {tab === 'input' && (
          span.input
            ? <pre className="text-sm whitespace-pre-wrap break-words p-3 bg-[var(--ao-bg)] rounded-md border border-[var(--ao-border)] max-h-[500px] overflow-auto">{formatJSON(span.input)}</pre>
            : <div className="text-center py-8 text-[var(--ao-text-muted)] text-sm">No input data</div>
        )}

        {tab === 'output' && (
          span.output
            ? <pre className="text-sm whitespace-pre-wrap break-words p-3 bg-[var(--ao-bg)] rounded-md border border-[var(--ao-border)] max-h-[500px] overflow-auto">{formatJSON(span.output)}</pre>
            : <div className="text-center py-8 text-[var(--ao-text-muted)] text-sm">No output data</div>
        )}
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
