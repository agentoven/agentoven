import { useEffect, useState } from 'react';

const API = '/api/v1';

interface Connector {
  id: string;
  name: string;
  kind: string;
  status: string;
  created_at: string;
}

export function ConnectorsPage() {
  const [connectors, setConnectors] = useState<Connector[]>([]);
  const [loading, setLoading] = useState(true);
  const [error, setError] = useState('');

  useEffect(() => {
    fetch(`${API}/connectors`)
      .then((r) => {
        if (!r.ok) throw new Error(`Server returned ${r.status}`);
        return r.json();
      })
      .then((data) => {
        // Backend returns { kitchen, connectors, note } — extract the array
        const list = Array.isArray(data) ? data : data?.connectors;
        setConnectors(Array.isArray(list) ? list : []);
      })
      .catch((e) => setError(e.message || 'Failed to load connectors'))
      .finally(() => setLoading(false));
  }, []);

  const connectorKinds = [
    { kind: 'snowflake', label: 'Snowflake', tier: 'Pro' },
    { kind: 'databricks', label: 'Databricks', tier: 'Pro' },
    { kind: 's3', label: 'S3 / ADLS / GCS', tier: 'Pro' },
    { kind: 'postgresql', label: 'PostgreSQL', tier: 'OSS' },
    { kind: 'http', label: 'HTTP / REST', tier: 'OSS' },
  ];

  return (
    <div className="p-8">
      <div className="flex items-center justify-between mb-6">
        <div>
          <h1 className="text-2xl font-bold text-[var(--ao-text)]">Data Connectors</h1>
          <p className="text-sm text-[var(--ao-text-muted)] mt-1">
            Connect to external data sources for RAG document ingestion
          </p>
        </div>
      </div>

      {/* Available connector types */}
      <div className="mb-8">
        <h2 className="text-sm font-semibold text-[var(--ao-text-muted)] uppercase tracking-wider mb-3">
          Available Types
        </h2>
        <div className="grid gap-3 sm:grid-cols-2 lg:grid-cols-3">
          {connectorKinds.map((ck) => (
            <div
              key={ck.kind}
              className="rounded-xl border border-[var(--ao-border)] bg-[var(--ao-surface)] p-4 flex items-center justify-between"
            >
              <div>
                <h3 className="font-medium text-[var(--ao-text)]">{ck.label}</h3>
                <p className="text-xs text-[var(--ao-text-muted)]">{ck.kind}</p>
              </div>
              <span
                className={`text-[10px] px-2 py-0.5 rounded-full font-medium ${
                  ck.tier === 'Pro'
                    ? 'bg-amber-500/20 text-amber-400'
                    : 'bg-green-500/20 text-green-400'
                }`}
              >
                {ck.tier}
              </span>
            </div>
          ))}
        </div>
      </div>

      {/* Active connectors */}
      <div>
        <h2 className="text-sm font-semibold text-[var(--ao-text-muted)] uppercase tracking-wider mb-3">
          Active Connectors
        </h2>

        {loading ? (
          <div className="text-[var(--ao-text-muted)]">Loading...</div>
        ) : error ? (
          <div className="rounded-lg border border-red-500/30 bg-red-500/10 p-3 text-sm text-red-400">
            {error}
          </div>
        ) : connectors.length === 0 ? (
          <div className="rounded-xl border border-[var(--ao-border)] bg-[var(--ao-surface)] p-8 text-center">
            <p className="text-[var(--ao-text-muted)] mb-2">No connectors configured</p>
            <p className="text-xs text-[var(--ao-text-muted)]">
              Use the API or CLI to create a data connector. Pro connectors (Snowflake, Databricks, S3)
              require an AgentOven Pro license.
            </p>
          </div>
        ) : (
          <div className="rounded-xl border border-[var(--ao-border)] overflow-hidden">
            <table className="w-full text-sm">
              <thead>
                <tr className="border-b border-[var(--ao-border)] bg-[var(--ao-surface)]">
                  <th className="text-left px-4 py-3 text-[var(--ao-text-muted)] font-medium">Name</th>
                  <th className="text-left px-4 py-3 text-[var(--ao-text-muted)] font-medium">Kind</th>
                  <th className="text-left px-4 py-3 text-[var(--ao-text-muted)] font-medium">Status</th>
                  <th className="text-left px-4 py-3 text-[var(--ao-text-muted)] font-medium">Created</th>
                </tr>
              </thead>
              <tbody>
                {connectors.map((c) => (
                  <tr key={c.id} className="border-b border-[var(--ao-border)] last:border-0">
                    <td className="px-4 py-3 text-[var(--ao-text)]">{c.name}</td>
                    <td className="px-4 py-3 text-[var(--ao-text-muted)]">{c.kind}</td>
                    <td className="px-4 py-3">
                      <span
                        className={`text-xs px-2 py-0.5 rounded-full ${
                          c.status === 'connected'
                            ? 'bg-green-500/20 text-green-400'
                            : 'bg-yellow-500/20 text-yellow-400'
                        }`}
                      >
                        {c.status}
                      </span>
                    </td>
                    <td className="px-4 py-3 text-[var(--ao-text-muted)]">
                      {new Date(c.created_at).toLocaleDateString()}
                    </td>
                  </tr>
                ))}
              </tbody>
            </table>
          </div>
        )}
      </div>
    </div>
  );
}
