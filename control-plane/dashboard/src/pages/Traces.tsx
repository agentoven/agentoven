import { useState, useEffect, useCallback } from 'react';
import { Activity, Search, Filter, X, RefreshCw, ChevronDown, ChevronUp } from 'lucide-react';
import { traces, type Trace } from '../api';
import {
  PageHeader, Card, StatusBadge, EmptyState,
  Spinner, ErrorBanner,
} from '../components/UI';

export function TracesPage() {
  const [data, setData] = useState<Trace[] | null>(null);
  const [loading, setLoading] = useState(true);
  const [error, setError] = useState<string | null>(null);

  // Filter state
  const [agentFilter, setAgentFilter] = useState('');
  const [recipeFilter, setRecipeFilter] = useState('');
  const [statusFilter, setStatusFilter] = useState('');
  const [showFilters, setShowFilters] = useState(false);
  const [expandedTrace, setExpandedTrace] = useState<string | null>(null);

  const fetchData = useCallback(() => {
    setLoading(true);
    setError(null);
    const filters: { agent?: string; recipe?: string; status?: string } = {};
    if (agentFilter) filters.agent = agentFilter;
    if (recipeFilter) filters.recipe = recipeFilter;
    if (statusFilter) filters.status = statusFilter;
    traces.list(filters)
      .then(setData)
      .catch((e) => setError(e.message))
      .finally(() => setLoading(false));
  }, [agentFilter, recipeFilter, statusFilter]);

  useEffect(() => { fetchData(); }, [fetchData]);

  const hasFilters = agentFilter || recipeFilter || statusFilter;

  const clearFilters = () => {
    setAgentFilter('');
    setRecipeFilter('');
    setStatusFilter('');
  };

  return (
    <div>
      <PageHeader
        title="Traces"
        description="Invocation traces with cost and token tracking"
      />

      {/* Filter Bar */}
      <div className="px-8 pt-4">
        <div className="flex items-center gap-3 mb-4">
          <button
            onClick={() => setShowFilters(!showFilters)}
            className="flex items-center gap-2 px-3 py-1.5 text-sm rounded-md border border-[var(--ao-border)] hover:bg-[var(--ao-surface-hover)] transition-colors"
          >
            <Filter size={14} />
            Filters
            {hasFilters && (
              <span className="ml-1 px-1.5 py-0.5 text-xs rounded-full bg-[var(--ao-accent)] text-white">
                {[agentFilter, recipeFilter, statusFilter].filter(Boolean).length}
              </span>
            )}
          </button>
          {hasFilters && (
            <button
              onClick={clearFilters}
              className="flex items-center gap-1 px-2 py-1 text-xs text-[var(--ao-text-muted)] hover:text-[var(--ao-text)] transition-colors"
            >
              <X size={12} /> Clear
            </button>
          )}
          <div className="flex-1" />
          <button
            onClick={fetchData}
            className="flex items-center gap-1.5 px-3 py-1.5 text-sm rounded-md border border-[var(--ao-border)] hover:bg-[var(--ao-surface-hover)] transition-colors"
          >
            <RefreshCw size={14} className={loading ? 'animate-spin' : ''} />
            Refresh
          </button>
        </div>

        {showFilters && (
          <Card className="mb-4 !p-4">
            <div className="grid grid-cols-3 gap-4">
              <div>
                <label className="block text-xs text-[var(--ao-text-muted)] mb-1 font-medium">Agent</label>
                <div className="relative">
                  <Search size={14} className="absolute left-2.5 top-1/2 -translate-y-1/2 text-[var(--ao-text-muted)]" />
                  <input
                    type="text"
                    value={agentFilter}
                    onChange={(e) => setAgentFilter(e.target.value)}
                    placeholder="Filter by agent name..."
                    className="w-full pl-8 pr-3 py-1.5 text-sm rounded-md border border-[var(--ao-border)] bg-[var(--ao-surface)] focus:outline-none focus:ring-1 focus:ring-[var(--ao-accent)]"
                  />
                </div>
              </div>
              <div>
                <label className="block text-xs text-[var(--ao-text-muted)] mb-1 font-medium">Recipe</label>
                <div className="relative">
                  <Search size={14} className="absolute left-2.5 top-1/2 -translate-y-1/2 text-[var(--ao-text-muted)]" />
                  <input
                    type="text"
                    value={recipeFilter}
                    onChange={(e) => setRecipeFilter(e.target.value)}
                    placeholder="Filter by recipe name..."
                    className="w-full pl-8 pr-3 py-1.5 text-sm rounded-md border border-[var(--ao-border)] bg-[var(--ao-surface)] focus:outline-none focus:ring-1 focus:ring-[var(--ao-accent)]"
                  />
                </div>
              </div>
              <div>
                <label className="block text-xs text-[var(--ao-text-muted)] mb-1 font-medium">Status</label>
                <select
                  value={statusFilter}
                  onChange={(e) => setStatusFilter(e.target.value)}
                  title="Filter by status"
                  className="w-full px-3 py-1.5 text-sm rounded-md border border-[var(--ao-border)] bg-[var(--ao-surface)] focus:outline-none focus:ring-1 focus:ring-[var(--ao-accent)]"
                >
                  <option value="">All statuses</option>
                  <option value="completed">Completed</option>
                  <option value="error">Error</option>
                </select>
              </div>
            </div>
          </Card>
        )}
      </div>

      {error && <ErrorBanner message={error} onRetry={fetchData} />}

      {loading ? (
        <Spinner />
      ) : data && data.length > 0 ? (
        <div className="px-8 pb-8">
          <Card className="overflow-hidden !p-0">
            <table className="w-full text-sm">
              <thead>
                <tr className="border-b border-[var(--ao-border)] text-left text-[var(--ao-text-muted)]">
                  <th className="px-4 py-3 font-medium w-8"></th>
                  <th className="px-4 py-3 font-medium">Agent</th>
                  <th className="px-4 py-3 font-medium">Recipe</th>
                  <th className="px-4 py-3 font-medium">Step</th>
                  <th className="px-4 py-3 font-medium">Status</th>
                  <th className="px-4 py-3 font-medium">Duration</th>
                  <th className="px-4 py-3 font-medium">Tokens</th>
                  <th className="px-4 py-3 font-medium">Cost (USD)</th>
                  <th className="px-4 py-3 font-medium">Time</th>
                </tr>
              </thead>
              <tbody>
                {data.map((trace) => {
                  const stepName = trace.metadata?.step_name as string || '—';
                  const isExpanded = expandedTrace === trace.id;
                  return (
                    <>
                      <tr
                        key={trace.id}
                        onClick={() => setExpandedTrace(isExpanded ? null : trace.id)}
                        className="border-b border-[var(--ao-border)] last:border-0 hover:bg-[var(--ao-surface-hover)] cursor-pointer"
                      >
                        <td className="px-4 py-3 text-[var(--ao-text-muted)]">
                          {isExpanded ? <ChevronUp size={14} /> : <ChevronDown size={14} />}
                        </td>
                        <td className="px-4 py-3 font-medium">{trace.agent_name}</td>
                        <td className="px-4 py-3 text-[var(--ao-text-muted)]">
                          {trace.recipe_name || '—'}
                        </td>
                        <td className="px-4 py-3 text-[var(--ao-text-muted)]">{stepName}</td>
                        <td className="px-4 py-3">
                          <StatusBadge status={trace.status} />
                        </td>
                        <td className="px-4 py-3">{trace.duration_ms}ms</td>
                        <td className="px-4 py-3">{trace.total_tokens.toLocaleString()}</td>
                        <td className="px-4 py-3">${trace.cost_usd.toFixed(4)}</td>
                        <td className="px-4 py-3 text-xs text-[var(--ao-text-muted)]">
                          {new Date(trace.created_at).toLocaleString()}
                        </td>
                      </tr>
                      {isExpanded && (
                        <tr key={trace.id + '-detail'} className="border-b border-[var(--ao-border)]">
                          <td colSpan={9} className="px-6 py-4 bg-[var(--ao-surface)]">
                            <div className="space-y-3">
                              <div className="grid grid-cols-2 gap-4 text-xs">
                                <div>
                                  <span className="text-[var(--ao-text-muted)]">Trace ID:</span>{' '}
                                  <code className="text-xs">{trace.id}</code>
                                </div>
                                {trace.metadata?.run_id && (
                                  <div>
                                    <span className="text-[var(--ao-text-muted)]">Run ID:</span>{' '}
                                    <code className="text-xs">{String(trace.metadata.run_id)}</code>
                                  </div>
                                )}
                              </div>
                              {trace.metadata?.error && (
                                <div className="text-sm text-red-400 bg-red-900/20 rounded px-3 py-2">
                                  {String(trace.metadata.error)}
                                </div>
                              )}
                            </div>
                          </td>
                        </tr>
                      )}
                    </>
                  );
                })}
              </tbody>
            </table>
          </Card>
          {/* Summary bar */}
          <div className="mt-3 flex items-center gap-4 text-xs text-[var(--ao-text-muted)]">
            <span>{data.length} trace{data.length !== 1 ? 's' : ''}</span>
            <span>·</span>
            <span>{data.reduce((s, t) => s + t.total_tokens, 0).toLocaleString()} total tokens</span>
            <span>·</span>
            <span>${data.reduce((s, t) => s + t.cost_usd, 0).toFixed(4)} total cost</span>
          </div>
        </div>
      ) : (
        <EmptyState
          icon={<Activity size={48} />}
          title={hasFilters ? 'No matching traces' : 'No traces yet'}
          description={hasFilters ? 'Try adjusting your filters.' : 'Traces will appear here when agents are invoked.'}
        />
      )}
    </div>
  );
}
