import { Link, useOutletContext } from 'react-router-dom';
import { Activity } from 'lucide-react';
import { traces, type Trace } from '../../api';
import { useAPI } from '../../hooks';
import { Card, EmptyState, ErrorBanner, Spinner } from '../../components/UI';
import type { WorkspaceContext } from './AgentWorkspace';

const dot = (status: string) =>
  status === 'completed' || status === 'ok' ? 'bg-emerald-400' : status === 'error' || status === 'failed' ? 'bg-red-400' : 'bg-slate-400';

/** Every run of this agent, newest first. Open one for the steps and tool calls. */
export default function AgentTraces() {
  const { agent } = useOutletContext<WorkspaceContext>();
  const { data, loading, error, refetch } = useAPI(() => traces.list({ agent: agent.name }));
  const list = [...(data ?? [])].sort((a, b) => (b.created_at ?? '').localeCompare(a.created_at ?? ''));
  return (
    <div className="px-4 sm:px-8 py-6 max-w-6xl space-y-4">
      {error && <ErrorBanner message={error} onRetry={refetch} />}
      {loading ? <Spinner /> : list.length === 0 ? (
        <EmptyState icon={<Activity size={44} />} title="No runs yet" description="Runs appear here as soon as someone tests or calls this agent." />
      ) : (
        <Card className="!p-0 overflow-x-auto">
          <div className="min-w-[640px]">
            <div className="grid gap-4 px-5 h-10 items-center border-b border-[var(--ao-border)] text-xs font-semibold uppercase tracking-wider text-[var(--ao-text-muted)]" style={{ gridTemplateColumns: '1.6fr 1.2fr 0.8fr 0.8fr 0.8fr' }}>
              <span>When</span><span>Result</span><span>Time</span><span>Tokens</span><span>Cost</span>
            </div>
            {list.map((t: Trace) => (
              <Link key={t.id} to={`/traces/${t.id}`} className="grid gap-4 px-5 py-3 items-center border-b border-[var(--ao-border)] last:border-b-0 text-sm hover:bg-[var(--ao-surface-hover)]/40" style={{ gridTemplateColumns: '1.6fr 1.2fr 0.8fr 0.8fr 0.8fr' }}>
                <span className="truncate">{t.created_at ? new Date(t.created_at).toLocaleString() : t.id}</span>
                <span className="flex items-center gap-2 min-w-0"><span className={`w-2 h-2 rounded-full shrink-0 ${dot(t.status)}`} /><span className="truncate capitalize">{t.status}</span></span>
                <span className="font-mono text-xs">{(t.duration_ms / 1000).toFixed(1)} s</span>
                <span className="font-mono text-xs">{(t.total_tokens ?? 0).toLocaleString()}</span>
                <span className="font-mono text-xs">${(t.cost_usd ?? 0).toFixed(4)}</span>
              </Link>
            ))}
          </div>
        </Card>
      )}
    </div>
  );
}
