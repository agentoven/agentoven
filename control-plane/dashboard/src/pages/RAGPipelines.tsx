import { useState } from 'react';

const API = '/api/v1';

type RAGStrategy = 'naive' | 'sentence_window' | 'parent_document' | 'hyde' | 'agentic';

interface SearchResult {
  id: string;
  content: string;
  score: number;
  metadata?: Record<string, string>;
}

interface RAGQueryResult {
  strategy: string;
  results: SearchResult[];
  total_chunks: number;
}

export function RAGPipelinesPage() {
  const [queryTab, setQueryTab] = useState<'query' | 'ingest'>('query');

  // Query state
  const [query, setQuery] = useState('');
  const [strategy, setStrategy] = useState<RAGStrategy>('naive');
  const [topK, setTopK] = useState(5);
  const [namespace, setNamespace] = useState('default');
  const [queryResult, setQueryResult] = useState<RAGQueryResult | null>(null);
  const [queryLoading, setQueryLoading] = useState(false);
  const [queryError, setQueryError] = useState('');

  // Ingest state
  const [ingestContent, setIngestContent] = useState('');
  const [ingestNamespace, setIngestNamespace] = useState('default');
  const [ingestLoading, setIngestLoading] = useState(false);
  const [ingestResult, setIngestResult] = useState<string | null>(null);
  const [ingestError, setIngestError] = useState('');

  const runQuery = async () => {
    setQueryLoading(true);
    setQueryError('');
    setQueryResult(null);
    try {
      const resp = await fetch(`${API}/rag/query`, {
        method: 'POST',
        headers: { 'Content-Type': 'application/json' },
        body: JSON.stringify({
          query,
          kitchen_id: '',
          namespace,
          strategy,
          top_k: topK,
        }),
      });
      if (!resp.ok) {
        const body = await resp.text();
        throw new Error(body || resp.statusText);
      }
      setQueryResult(await resp.json());
    } catch (e: unknown) {
      setQueryError(e instanceof Error ? e.message : 'Query failed');
    } finally {
      setQueryLoading(false);
    }
  };

  const runIngest = async () => {
    setIngestLoading(true);
    setIngestError('');
    setIngestResult(null);
    try {
      const resp = await fetch(`${API}/rag/ingest`, {
        method: 'POST',
        headers: { 'Content-Type': 'application/json' },
        body: JSON.stringify({
          kitchen_id: '',
          namespace: ingestNamespace,
          documents: [{ id: crypto.randomUUID(), content: ingestContent, metadata: {} }],
        }),
      });
      if (!resp.ok) {
        const body = await resp.text();
        throw new Error(body || resp.statusText);
      }
      const result = await resp.json();
      setIngestResult(
        `Ingested ${result.documents_processed} doc(s), ${result.chunks_created} chunk(s), ${result.vectors_stored} vector(s)`
      );
    } catch (e: unknown) {
      setIngestError(e instanceof Error ? e.message : 'Ingest failed');
    } finally {
      setIngestLoading(false);
    }
  };

  const strategies: { value: RAGStrategy; label: string; desc: string }[] = [
    { value: 'naive', label: 'Naive', desc: 'Direct embed → search' },
    { value: 'sentence_window', label: 'Sentence Window', desc: 'Expand context around matches' },
    { value: 'parent_document', label: 'Parent Document', desc: 'Return full parent document' },
    { value: 'hyde', label: 'HyDE', desc: 'Hypothetical answer → embed → search' },
    { value: 'agentic', label: 'Agentic', desc: 'Query decomposition + re-rank' },
  ];

  return (
    <div className="p-8">
      <div className="mb-6">
        <h1 className="text-2xl font-bold text-[var(--ao-text)]">RAG Pipelines</h1>
        <p className="text-sm text-[var(--ao-text-muted)] mt-1">
          Retrieval-Augmented Generation with 5 retrieval strategies
        </p>
      </div>

      {/* Tabs */}
      <div className="flex gap-1 mb-6 border-b border-[var(--ao-border)]">
        {(['query', 'ingest'] as const).map((tab) => (
          <button
            key={tab}
            onClick={() => setQueryTab(tab)}
            className={`px-4 py-2 text-sm font-medium capitalize border-b-2 transition-colors ${
              queryTab === tab
                ? 'border-[var(--ao-primary)] text-[var(--ao-primary)]'
                : 'border-transparent text-[var(--ao-text-muted)] hover:text-[var(--ao-text)]'
            }`}
          >
            {tab}
          </button>
        ))}
      </div>

      {queryTab === 'query' && (
        <div className="space-y-4 max-w-3xl">
          <div>
            <label className="block text-sm font-medium text-[var(--ao-text)] mb-1">Query</label>
            <textarea
              value={query}
              onChange={(e) => setQuery(e.target.value)}
              placeholder="Enter your search query..."
              rows={3}
              className="w-full rounded-lg border border-[var(--ao-border)] bg-[var(--ao-bg)] text-[var(--ao-text)] px-3 py-2 text-sm focus:outline-none focus:ring-2 focus:ring-[var(--ao-primary)]/50"
            />
          </div>

          <div className="grid grid-cols-3 gap-4">
            <div>
              <label className="block text-sm font-medium text-[var(--ao-text)] mb-1">Strategy</label>
              <select
                value={strategy}
                onChange={(e) => setStrategy(e.target.value as RAGStrategy)}
                className="w-full rounded-lg border border-[var(--ao-border)] bg-[var(--ao-bg)] text-[var(--ao-text)] px-3 py-2 text-sm"
              >
                {strategies.map((s) => (
                  <option key={s.value} value={s.value}>
                    {s.label}
                  </option>
                ))}
              </select>
              <p className="text-xs text-[var(--ao-text-muted)] mt-1">
                {strategies.find((s) => s.value === strategy)?.desc}
              </p>
            </div>
            <div>
              <label className="block text-sm font-medium text-[var(--ao-text)] mb-1">Top K</label>
              <input
                type="number"
                value={topK}
                onChange={(e) => setTopK(Number(e.target.value))}
                min={1}
                max={100}
                className="w-full rounded-lg border border-[var(--ao-border)] bg-[var(--ao-bg)] text-[var(--ao-text)] px-3 py-2 text-sm"
              />
            </div>
            <div>
              <label className="block text-sm font-medium text-[var(--ao-text)] mb-1">Namespace</label>
              <input
                type="text"
                value={namespace}
                onChange={(e) => setNamespace(e.target.value)}
                className="w-full rounded-lg border border-[var(--ao-border)] bg-[var(--ao-bg)] text-[var(--ao-text)] px-3 py-2 text-sm"
              />
            </div>
          </div>

          <button
            onClick={runQuery}
            disabled={!query.trim() || queryLoading}
            className="px-4 py-2 rounded-lg bg-[var(--ao-primary)] text-white text-sm font-medium hover:opacity-90 disabled:opacity-50 disabled:cursor-not-allowed"
          >
            {queryLoading ? 'Searching...' : 'Search'}
          </button>

          {queryError && (
            <div className="rounded-lg border border-red-500/30 bg-red-500/10 p-3 text-sm text-red-400">
              {queryError}
            </div>
          )}

          {queryResult && (
            <div className="space-y-3">
              <div className="flex items-center gap-3 text-sm text-[var(--ao-text-muted)]">
                <span>Strategy: {queryResult.strategy}</span>
                <span>•</span>
                <span>{queryResult.results.length} results</span>
                <span>•</span>
                <span>{queryResult.total_chunks} total chunks</span>
              </div>
              {queryResult.results.map((r, i) => (
                <div
                  key={r.id}
                  className="rounded-xl border border-[var(--ao-border)] bg-[var(--ao-surface)] p-4"
                >
                  <div className="flex items-center justify-between mb-2">
                    <span className="text-xs text-[var(--ao-text-muted)]">#{i + 1}</span>
                    <span className="text-xs px-2 py-0.5 rounded-full bg-[var(--ao-primary)]/20 text-[var(--ao-primary)]">
                      {(r.score * 100).toFixed(1)}% match
                    </span>
                  </div>
                  <p className="text-sm text-[var(--ao-text)] whitespace-pre-wrap">{r.content}</p>
                </div>
              ))}
            </div>
          )}
        </div>
      )}

      {queryTab === 'ingest' && (
        <div className="space-y-4 max-w-3xl">
          <div>
            <label className="block text-sm font-medium text-[var(--ao-text)] mb-1">
              Document Content
            </label>
            <textarea
              value={ingestContent}
              onChange={(e) => setIngestContent(e.target.value)}
              placeholder="Paste text content to ingest into the vector store..."
              rows={8}
              className="w-full rounded-lg border border-[var(--ao-border)] bg-[var(--ao-bg)] text-[var(--ao-text)] px-3 py-2 text-sm font-mono focus:outline-none focus:ring-2 focus:ring-[var(--ao-primary)]/50"
            />
          </div>

          <div>
            <label className="block text-sm font-medium text-[var(--ao-text)] mb-1">Namespace</label>
            <input
              type="text"
              value={ingestNamespace}
              onChange={(e) => setIngestNamespace(e.target.value)}
              className="w-full max-w-xs rounded-lg border border-[var(--ao-border)] bg-[var(--ao-bg)] text-[var(--ao-text)] px-3 py-2 text-sm"
            />
          </div>

          <button
            onClick={runIngest}
            disabled={!ingestContent.trim() || ingestLoading}
            className="px-4 py-2 rounded-lg bg-[var(--ao-primary)] text-white text-sm font-medium hover:opacity-90 disabled:opacity-50 disabled:cursor-not-allowed"
          >
            {ingestLoading ? 'Ingesting...' : 'Ingest Document'}
          </button>

          {ingestError && (
            <div className="rounded-lg border border-red-500/30 bg-red-500/10 p-3 text-sm text-red-400">
              {ingestError}
            </div>
          )}

          {ingestResult && (
            <div className="rounded-lg border border-green-500/30 bg-green-500/10 p-3 text-sm text-green-400">
              ✓ {ingestResult}
            </div>
          )}
        </div>
      )}
    </div>
  );
}
