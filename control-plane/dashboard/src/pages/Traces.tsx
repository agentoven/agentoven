import { Activity } from 'lucide-react';
import { traces } from '../api';
import { useAPI } from '../hooks';
import {
  PageHeader, Card, StatusBadge, EmptyState,
  Spinner, ErrorBanner,
} from '../components/UI';

export function TracesPage() {
  const { data, loading, error, refetch } = useAPI(traces.list);

  return (
    <div>
      <PageHeader
        title="Traces"
        description="Invocation traces with cost and token tracking"
      />

      {error && <ErrorBanner message={error} onRetry={refetch} />}

      {loading ? (
        <Spinner />
      ) : data && data.length > 0 ? (
        <div className="p-8">
          <Card className="overflow-hidden !p-0">
            <table className="w-full text-sm">
              <thead>
                <tr className="border-b border-[var(--ao-border)] text-left text-[var(--ao-text-muted)]">
                  <th className="px-4 py-3 font-medium">Agent</th>
                  <th className="px-4 py-3 font-medium">Recipe</th>
                  <th className="px-4 py-3 font-medium">Status</th>
                  <th className="px-4 py-3 font-medium">Duration</th>
                  <th className="px-4 py-3 font-medium">Tokens</th>
                  <th className="px-4 py-3 font-medium">Cost (USD)</th>
                  <th className="px-4 py-3 font-medium">Time</th>
                </tr>
              </thead>
              <tbody>
                {data.map((trace) => (
                  <tr
                    key={trace.id}
                    className="border-b border-[var(--ao-border)] last:border-0 hover:bg-[var(--ao-surface-hover)]"
                  >
                    <td className="px-4 py-3 font-medium">{trace.agent_name}</td>
                    <td className="px-4 py-3 text-[var(--ao-text-muted)]">
                      {trace.recipe_name || '—'}
                    </td>
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
                ))}
              </tbody>
            </table>
          </Card>
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
