import { Bot, BookOpen, Cpu, Wrench, Activity } from 'lucide-react';
import { agents, recipes, providers, tools, traces } from '../api';
import { useAPI } from '../hooks';
import { PageHeader, StatCard, Spinner, ErrorBanner, Card, StatusBadge } from '../components/UI';

export function OverviewPage() {
  const a = useAPI(agents.list);
  const r = useAPI(recipes.list);
  const p = useAPI(providers.list);
  const t = useAPI(tools.list);
  const tr = useAPI(traces.list);

  const loading = a.loading || r.loading || p.loading || t.loading || tr.loading;
  const error = a.error || r.error || p.error || t.error || tr.error;

  return (
    <div>
      <PageHeader
        title="Dashboard"
        description="Your AgentOven kitchen at a glance"
      />

      {error && <ErrorBanner message={error} />}
      {loading ? (
        <Spinner />
      ) : (
        <div className="p-8 space-y-8">
          {/* Stat cards */}
          <div className="grid grid-cols-2 md:grid-cols-3 lg:grid-cols-5 gap-4">
            <StatCard label="Agents" value={a.data?.length ?? 0} icon={<Bot size={24} />} />
            <StatCard label="Recipes" value={r.data?.length ?? 0} icon={<BookOpen size={24} />} />
            <StatCard label="Providers" value={p.data?.length ?? 0} icon={<Cpu size={24} />} />
            <StatCard label="MCP Tools" value={t.data?.length ?? 0} icon={<Wrench size={24} />} />
            <StatCard label="Traces" value={tr.data?.length ?? 0} icon={<Activity size={24} />} />
          </div>

          {/* Recent agents */}
          <div>
            <h2 className="text-base font-semibold mb-3">Recent Agents</h2>
            {a.data && a.data.length > 0 ? (
              <div className="grid grid-cols-1 md:grid-cols-2 lg:grid-cols-3 gap-4">
                {a.data.slice(0, 6).map((agent) => (
                  <Card key={agent.id}>
                    <div className="flex items-start justify-between">
                      <div>
                        <p className="font-medium">{agent.name}</p>
                        <p className="text-xs text-[var(--ao-text-muted)] mt-1">
                          {agent.framework || 'Unknown framework'} · v{agent.version}
                        </p>
                      </div>
                      <StatusBadge status={agent.status} />
                    </div>
                    {agent.description && (
                      <p className="text-sm text-[var(--ao-text-muted)] mt-2 line-clamp-2">
                        {agent.description}
                      </p>
                    )}
                  </Card>
                ))}
              </div>
            ) : (
              <p className="text-sm text-[var(--ao-text-muted)]">No agents registered yet.</p>
            )}
          </div>

          {/* Recent traces */}
          <div>
            <h2 className="text-base font-semibold mb-3">Recent Traces</h2>
            {tr.data && tr.data.length > 0 ? (
              <Card className="overflow-hidden !p-0">
                <table className="w-full text-sm">
                  <thead>
                    <tr className="border-b border-[var(--ao-border)] text-left text-[var(--ao-text-muted)]">
                      <th className="px-4 py-3 font-medium">Agent</th>
                      <th className="px-4 py-3 font-medium">Status</th>
                      <th className="px-4 py-3 font-medium">Duration</th>
                      <th className="px-4 py-3 font-medium">Tokens</th>
                      <th className="px-4 py-3 font-medium">Cost</th>
                    </tr>
                  </thead>
                  <tbody>
                    {tr.data.slice(0, 5).map((trace) => (
                      <tr key={trace.id} className="border-b border-[var(--ao-border)] last:border-0">
                        <td className="px-4 py-3">{trace.agent_name}</td>
                        <td className="px-4 py-3"><StatusBadge status={trace.status} /></td>
                        <td className="px-4 py-3">{trace.duration_ms}ms</td>
                        <td className="px-4 py-3">{trace.total_tokens.toLocaleString()}</td>
                        <td className="px-4 py-3">${trace.cost_usd.toFixed(4)}</td>
                      </tr>
                    ))}
                  </tbody>
                </table>
              </Card>
            ) : (
              <p className="text-sm text-[var(--ao-text-muted)]">No traces recorded yet.</p>
            )}
          </div>
        </div>
      )}
    </div>
  );
}
