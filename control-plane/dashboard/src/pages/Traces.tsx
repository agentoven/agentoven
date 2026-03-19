import { useState, useEffect, useCallback } from 'react';
import { useNavigate } from 'react-router-dom';
import { Activity, RefreshCw } from 'lucide-react';
import { traces, type Trace } from '../api';
import {
  PageHeader, Card, StatusBadge, EmptyState,
  Spinner, ErrorBanner,
} from '../components/UI';

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

export function TracesPage() {
  const navigate = useNavigate();
  const [data, setData] = useState<Trace[] | null>(null);
  const [loading, setLoading] = useState(true);
  const [error, setError] = useState<string | null>(null);
  const [statusFilter, setStatusFilter] = useState('');
  const [agentFilter, setAgentFilter] = useState('');

  const fetchData = useCallback(() => {
    setLoading(true);
    setError(null);
    const filters: { agent?: string; status?: string } = {};
    if (agentFilter) filters.agent = agentFilter;
    if (statusFilter) filters.status = statusFilter;
    traces.list(filters)
      .then(setData)
      .catch((e) => setError(e.message))
      .finally(() => setLoading(false));
  }, [agentFilter, statusFilter]);

  useEffect(() => { fetchData(); }, [fetchData]);

  // Compute stats
  const totalTraces = data?.length ?? 0;
  const avgDuration = totalTraces > 0 ? (data!.reduce((s, t) => s + t.duration_ms, 0) / totalTraces) : 0;
  const totalTokens = data?.reduce((s, t) => s + (t.total_tokens || 0), 0) ?? 0;
  const totalCost = data?.reduce((s, t) => s + (t.cost_usd || 0), 0) ?? 0;

  // Unique agents for filter
  const agents = [...new Set(data?.map((t) => t.agent_name).filter(Boolean) ?? [])];

  return (
    <div>
      <PageHeader title="Traces" description="Invocation traces with cost and token tracking" />

      {/* Stats */}
      <div className="px-8 pt-4 grid grid-cols-4 gap-3 mb-4">
        <Card><div className="text-xs text-[var(--ao-text-muted)] uppercase tracking-wider">Traces</div><div className="text-2xl font-bold mt-0.5">{totalTraces}</div></Card>
        <Card><div className="text-xs text-[var(--ao-text-muted)] uppercase tracking-wider">Avg Duration</div><div className="text-2xl font-bold mt-0.5">{formatDuration(avgDuration)}</div></Card>
        <Card><div className="text-xs text-[var(--ao-text-muted)] uppercase tracking-wider">Total Tokens</div><div className="text-2xl font-bold mt-0.5">{formatTokens(totalTokens)}</div></Card>
        <Card><div className="text-xs text-[var(--ao-text-muted)] uppercase tracking-wider">Total Cost</div><div className="text-2xl font-bold mt-0.5">${totalCost.toFixed(4)}</div></Card>
      </div>

      {/* Filters */}
      <div className="px-8 flex items-center gap-3 mb-4">
        <select
          value={statusFilter}
          onChange={(e) => setStatusFilter(e.target.value)}
          title="Filter by status"
          className="px-3 py-1.5 text-sm rounded-md border border-[var(--ao-border)] bg-[var(--ao-surface)]"
        >
          <option value="">All statuses</option>
          <option value="completed">Completed</option>
          <option value="error">Error</option>
        </select>
        <select
          value={agentFilter}
          onChange={(e) => setAgentFilter(e.target.value)}
          title="Filter by agent"
          className="px-3 py-1.5 text-sm rounded-md border border-[var(--ao-border)] bg-[var(--ao-surface)]"
        >
          <option value="">All agents</option>
          {agents.map((a) => <option key={a} value={a}>{a}</option>)}
        </select>
        <div className="flex-1" />
        <button
          onClick={fetchData}
          className="flex items-center gap-1.5 px-3 py-1.5 text-sm rounded-md border border-[var(--ao-border)] hover:bg-[var(--ao-surface-hover)] transition-colors"
        >
          <RefreshCw size={14} className={loading ? 'animate-spin' : ''} />
          Refresh
        </button>
      </div>

      {error && <ErrorBanner message={error} onRetry={fetchData} />}

      {loading ? (
        <Spinner />
      ) : data && data.length > 0 ? (
        <div className="px-8 pb-8 flex flex-col gap-2">
          {data.map((trace) => {
            const isAgent = !trace.recipe_name;
            return (
              <div
                key={trace.id}
                onClick={() => navigate(`/traces/${trace.id}`)}
                className="flex items-center gap-4 bg-[var(--ao-surface)] border border-[var(--ao-border)] rounded-lg p-4 cursor-pointer hover:border-[var(--ao-brand)] transition-colors"
                style={{ borderLeft: `3px solid ${isAgent ? '#3b82f6' : '#8b5cf6'}` }}
              >
                <div className="flex-1 min-w-0">
                  <div className="flex items-center gap-2 mb-1">
                    <span className="font-semibold text-sm">{trace.agent_name}</span>
                    {trace.recipe_name && (
                      <span className="text-xs text-[var(--ao-text-muted)]">/ {trace.recipe_name}</span>
                    )}
                    <StatusBadge status={trace.status} />
                  </div>
                  {trace.input_text && (
                    <div className="text-xs text-[var(--ao-text-muted)] truncate max-w-xl">
                      {trace.input_text.slice(0, 120)}
                    </div>
                  )}
                </div>
                <div className="flex gap-6 items-center text-xs text-[var(--ao-text-muted)] shrink-0">
                  <div className="text-center"><div className="font-bold text-[var(--ao-text)]">{formatDuration(trace.duration_ms)}</div><div>duration</div></div>
                  <div className="text-center"><div className="font-bold text-[var(--ao-text)]">{formatTokens(trace.total_tokens)}</div><div>tokens</div></div>
                  <div className="text-center"><div className="font-bold text-[var(--ao-text)]">${(trace.cost_usd ?? 0).toFixed(4)}</div><div>cost</div></div>
                  <div className="w-16 text-right">{timeAgo(trace.created_at)}</div>
                </div>
              </div>
            );
          })}
        </div>
      ) : (
        <EmptyState
          icon={<Activity size={48} />}
          title="No traces yet"
          description="Traces will appear here when agents are invoked."
        />
      )}
    </div>
  );
}
