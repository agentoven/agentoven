import { useEffect, useMemo, useState } from 'react';
import { useSearchParams, useNavigate, Link } from 'react-router-dom';
import { traces, type Trace, type Span } from '../api';
import { PageHeader, Card, StatusBadge, Spinner, ErrorBanner } from '../components/UI';
import { formatDuration, formatTokens, formatCost, modelOf } from './Traces';

/** How much one number differs from another, as text: "+12%" or "-3%". */
function delta(a: number, b: number): string {
  if (!a && !b) return '';
  if (!a) return 'new';
  const pct = ((b - a) / a) * 100;
  if (Math.abs(pct) < 0.5) return '=';
  return `${pct > 0 ? '+' : ''}${pct.toFixed(Math.abs(pct) < 10 ? 1 : 0)}%`;
}

function Metric({ label, a, b, fmt, lowerIsBetter = true }: { label: string; a: number; b: number; fmt: (n: number) => string; lowerIsBetter?: boolean }) {
  const d = delta(a, b);
  const better = b === a ? null : (b < a) === lowerIsBetter;
  return (
    <div className="grid grid-cols-[110px_1fr_1fr_70px] gap-3 py-2 border-b border-[var(--ao-border)] text-sm items-center">
      <span className="text-[var(--ao-text-muted)]">{label}</span>
      <span className="font-medium">{fmt(a)}</span>
      <span className="font-medium">{fmt(b)}</span>
      <span className={`text-xs font-semibold ${better === null ? 'text-[var(--ao-text-muted)]' : better ? 'text-emerald-400' : 'text-red-400'}`}>{d}</span>
    </div>
  );
}

function sameText(a?: string, b?: string): boolean { return (a ?? '').trim() === (b ?? '').trim(); }

function spanLine(s: Span | undefined) {
  if (!s) return <span className="text-[var(--ao-text-muted)]">—</span>;
  return (
    <span className="flex items-center gap-2 min-w-0">
      <span className="truncate font-medium">{s.name}</span>
      {s.model && <span className="text-xs text-[var(--ao-text-muted)] truncate">{s.model}</span>}
      <span className="text-xs text-[var(--ao-text-muted)] shrink-0">{formatDuration(s.duration_ms)}{s.usage?.total_tokens ? ` · ${formatTokens(s.usage.total_tokens)} tok` : ''}{s.usage?.estimated_cost_usd ? ` · ${formatCost(s.usage.estimated_cost_usd)}` : ''}</span>
    </span>
  );
}

/** Two traces side by side: what each took, cost and said, and how their spans line up. */
export function TraceComparePage() {
  const [params, setParams] = useSearchParams();
  const navigate = useNavigate();
  const idA = params.get('a') ?? '';
  const idB = params.get('b') ?? '';
  const [a, setA] = useState<Trace | null>(null);
  const [b, setB] = useState<Trace | null>(null);
  const [error, setError] = useState<string | null>(null);
  const [recent, setRecent] = useState<Trace[]>([]);

  useEffect(() => { traces.list({ limit: 50, roots: false }).then(setRecent).catch(() => undefined); }, []);
  useEffect(() => {
    setError(null);
    if (idA) traces.get(idA).then(setA).catch((e) => setError(e.message)); else setA(null);
    if (idB) traces.get(idB).then(setB).catch((e) => setError(e.message)); else setB(null);
  }, [idA, idB]);

  const rows = useMemo(() => {
    const sa = a?.spans ?? [], sb = b?.spans ?? [];
    const used = new Set<number>();
    const out: { a?: Span; b?: Span }[] = [];
    for (const x of sa) {
      const j = sb.findIndex((y, i) => !used.has(i) && y.kind === x.kind && y.name === x.name);
      if (j >= 0) { used.add(j); out.push({ a: x, b: sb[j] }); } else out.push({ a: x });
    }
    sb.forEach((y, i) => { if (!used.has(i)) out.push({ b: y }); });
    return out;
  }, [a, b]);

  const pick = (key: 'a' | 'b') => (
    <select value={key === 'a' ? idA : idB} onChange={(e) => { const n = new URLSearchParams(params); n.set(key, e.target.value); setParams(n, { replace: true }); }}
      className="px-2.5 py-1.5 text-sm rounded-md border border-[var(--ao-border)] bg-[var(--ao-surface)] w-full">
      <option value="">Choose a trace…</option>
      {recent.map((t) => <option key={t.id} value={t.id}>{t.agent_name} · {new Date(t.created_at).toLocaleString()} · {t.id.slice(0, 8)}</option>)}
      {(key === 'a' ? a : b) && !recent.some((t) => t.id === (key === 'a' ? idA : idB)) && <option value={key === 'a' ? idA : idB}>{(key === 'a' ? a : b)!.agent_name} · {(key === 'a' ? idA : idB).slice(0, 8)}</option>}
    </select>
  );

  return (
    <div>
      <PageHeader title="Compare traces" description="Latency, tokens, cost, output and spans of two traces, side by side"
        action={<button onClick={() => navigate('/traces')} className="px-3 py-1.5 text-sm rounded-md border border-[var(--ao-border)] hover:bg-[var(--ao-surface-hover)]">← Traces</button>} />
      <div className="px-8 pt-4">
        {error && <ErrorBanner message={error} />}
        <div className="grid grid-cols-2 gap-3 mb-4">
          <div>{pick('a')}</div><div>{pick('b')}</div>
        </div>
        {(idA && !a) || (idB && !b) ? <Spinner /> : a && b ? (
          <>
            <Card className="mb-4">
              <div className="grid grid-cols-[110px_1fr_1fr_70px] gap-3 pb-2 border-b border-[var(--ao-border)] text-sm items-center">
                <span />
                <Link to={`/traces/${a.id}`} className="font-semibold hover:underline">{a.agent_name} <StatusBadge status={a.status} /></Link>
                <Link to={`/traces/${b.id}`} className="font-semibold hover:underline">{b.agent_name} <StatusBadge status={b.status} /></Link>
                <span className="text-xs text-[var(--ao-text-muted)]">B vs A</span>
              </div>
              <div className="grid grid-cols-[110px_1fr_1fr_70px] gap-3 py-2 border-b border-[var(--ao-border)] text-sm">
                <span className="text-[var(--ao-text-muted)]">Model</span><span>{modelOf(a) || '—'}</span><span>{modelOf(b) || '—'}</span><span />
              </div>
              <Metric label="Latency" a={a.duration_ms} b={b.duration_ms} fmt={formatDuration} />
              <Metric label="Total tokens" a={a.total_tokens} b={b.total_tokens} fmt={formatTokens} />
              <Metric label="Input tokens" a={a.usage?.input_tokens ?? 0} b={b.usage?.input_tokens ?? 0} fmt={formatTokens} />
              <Metric label="Output tokens" a={a.usage?.output_tokens ?? 0} b={b.usage?.output_tokens ?? 0} fmt={formatTokens} />
              <Metric label="Cost" a={a.cost_usd ?? 0} b={b.cost_usd ?? 0} fmt={formatCost} />
              <Metric label="Spans" a={a.spans?.length ?? 0} b={b.spans?.length ?? 0} fmt={String} lowerIsBetter={false} />
            </Card>

            {([['Input', a.input_text, b.input_text], ['Output', a.output_text, b.output_text]] as const).map(([label, ta, tb]) => (
              <Card key={label} className="mb-4">
                <div className="flex items-center justify-between mb-2">
                  <span className="text-[0.7rem] text-[var(--ao-text-muted)] uppercase tracking-wider">{label}</span>
                  <span className={`text-xs ${sameText(ta, tb) ? 'text-emerald-400' : 'text-amber-400'}`}>{sameText(ta, tb) ? 'identical' : 'different'}</span>
                </div>
                <div className="grid grid-cols-2 gap-3">
                  {[ta, tb].map((t, i) => <pre key={i} className="text-sm whitespace-pre-wrap break-words max-h-64 overflow-auto p-2 rounded bg-[var(--ao-bg)] border border-[var(--ao-border)]">{t || '—'}</pre>)}
                </div>
              </Card>
            ))}

            <Card className="mb-8">
              <div className="text-[0.7rem] text-[var(--ao-text-muted)] uppercase tracking-wider mb-2">Spans</div>
              {rows.length === 0 ? <div className="text-sm text-[var(--ao-text-muted)]">Neither trace has spans.</div> : rows.map((r, i) => (
                <div key={i} className="grid grid-cols-2 gap-3 py-1.5 border-t border-[var(--ao-border)] text-sm">
                  {spanLine(r.a)}{spanLine(r.b)}
                </div>
              ))}
            </Card>
          </>
        ) : <div className="text-sm text-[var(--ao-text-muted)]">Choose two traces to compare.</div>}
      </div>
    </div>
  );
}
