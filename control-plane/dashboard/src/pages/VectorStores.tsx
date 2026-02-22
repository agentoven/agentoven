import { useEffect, useState } from 'react';

const API = '/api/v1';

const safeFetch = (url: string) =>
  fetch(url).then((r) => {
    if (!r.ok) throw new Error(`Server returned ${r.status}`);
    return r.json();
  });

export function VectorStoresPage() {
  const [drivers, setDrivers] = useState<string[]>([]);
  const [health, setHealth] = useState<Record<string, string>>({});
  const [loading, setLoading] = useState(true);
  const [error, setError] = useState('');

  useEffect(() => {
    Promise.all([
      safeFetch(`${API}/vectorstores`),
      safeFetch(`${API}/vectorstores/health`),
    ])
      .then(([d, h]) => {
        setDrivers(Array.isArray(d) ? d : []);
        setHealth(h || {});
      })
      .catch((e) => setError(e.message || 'Failed to load vector stores'))
      .finally(() => setLoading(false));
  }, []);

  return (
    <div className="p-8">
      <div className="flex items-center justify-between mb-6">
        <div>
          <h1 className="text-2xl font-bold text-[var(--ao-text)]">Vector Stores</h1>
          <p className="text-sm text-[var(--ao-text-muted)] mt-1">
            Registered vector store backends for similarity search
          </p>
        </div>
      </div>

      {loading ? (
        <div className="text-[var(--ao-text-muted)]">Loading...</div>
      ) : error ? (
        <div className="rounded-lg border border-red-500/30 bg-red-500/10 p-3 text-sm text-red-400">
          {error}
        </div>
      ) : drivers.length === 0 ? (
        <div className="rounded-xl border border-[var(--ao-border)] bg-[var(--ao-surface)] p-8 text-center">
          <p className="text-[var(--ao-text-muted)] mb-2">No vector store drivers registered</p>
          <p className="text-xs text-[var(--ao-text-muted)]">
            The embedded in-memory store is always registered. Check server logs for details.
          </p>
        </div>
      ) : (
        <div className="grid gap-4 sm:grid-cols-2 lg:grid-cols-3">
          {drivers.map((name) => {
            const isEmbedded = name === 'embedded';
            const isPgvector = name === 'pgvector';
            return (
              <div
                key={name}
                className="rounded-xl border border-[var(--ao-border)] bg-[var(--ao-surface)] p-5"
              >
                <div className="flex items-center justify-between mb-3">
                  <div className="flex items-center gap-2">
                    <h3 className="font-semibold text-[var(--ao-text)]">{name}</h3>
                    {isEmbedded && (
                      <span className="text-[10px] px-1.5 py-0.5 rounded bg-[var(--ao-primary)]/20 text-[var(--ao-primary)]">
                        Built-in
                      </span>
                    )}
                    {isPgvector && (
                      <span className="text-[10px] px-1.5 py-0.5 rounded bg-purple-500/20 text-purple-400">
                        External
                      </span>
                    )}
                  </div>
                  <span
                    className={`text-xs px-2 py-0.5 rounded-full ${
                      health[name] === 'ok'
                        ? 'bg-green-500/20 text-green-400'
                        : 'bg-red-500/20 text-red-400'
                    }`}
                  >
                    {health[name] === 'ok' ? '● Healthy' : '● Error'}
                  </span>
                </div>
                <p className="text-xs text-[var(--ao-text-muted)]">
                  {isEmbedded && 'In-memory brute-force cosine similarity (50K vectors max)'}
                  {isPgvector && 'PostgreSQL + pgvector extension (scalable, persistent)'}
                  {!isEmbedded && !isPgvector && 'Custom vector store driver'}
                </p>
                {health[name] && health[name] !== 'ok' && (
                  <p className="text-xs text-red-400 mt-2">{health[name]}</p>
                )}
              </div>
            );
          })}
        </div>
      )}
    </div>
  );
}
