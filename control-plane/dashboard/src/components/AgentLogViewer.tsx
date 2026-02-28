import { useState, useEffect, useRef, useCallback } from 'react';
import { Terminal, X, Pause, Play, Trash2 } from 'lucide-react';
import { agents, type LogEntry } from '../api';

interface AgentLogViewerProps {
  agentName: string;
  onClose: () => void;
}

export function AgentLogViewer({ agentName, onClose }: AgentLogViewerProps) {
  const [logs, setLogs] = useState<LogEntry[]>([]);
  const [paused, setPaused] = useState(false);
  const [connected, setConnected] = useState(false);
  const [error, setError] = useState<string | null>(null);
  const logsEndRef = useRef<HTMLDivElement>(null);
  const eventSourceRef = useRef<EventSource | null>(null);
  const pausedRef = useRef(paused);

  useEffect(() => { pausedRef.current = paused; }, [paused]);

  const scrollToBottom = useCallback(() => {
    if (!pausedRef.current) {
      logsEndRef.current?.scrollIntoView({ behavior: 'smooth' });
    }
  }, []);

  useEffect(() => {
    const url = agents.logsStreamURL(agentName);
    const es = new EventSource(url);
    eventSourceRef.current = es;

    es.onopen = () => {
      setConnected(true);
      setError(null);
    };

    es.onmessage = (event) => {
      try {
        const entry: LogEntry = JSON.parse(event.data);
        setLogs((prev) => {
          const next = [...prev, entry];
          // Keep last 2000 entries in the UI
          if (next.length > 2000) return next.slice(-2000);
          return next;
        });
      } catch {
        // ignore parse errors
      }
    };

    es.onerror = () => {
      setConnected(false);
      setError('Connection lost — retrying...');
    };

    return () => {
      es.close();
      eventSourceRef.current = null;
    };
  }, [agentName]);

  useEffect(() => {
    scrollToBottom();
  }, [logs, scrollToBottom]);

  const clearLogs = () => setLogs([]);

  return (
    <div className="fixed inset-0 z-50 flex items-end justify-center bg-black/50 backdrop-blur-sm">
      <div className="w-full max-w-5xl h-[60vh] flex flex-col bg-[var(--ao-bg)] border border-[var(--ao-border)] rounded-t-xl shadow-2xl">
        {/* Header */}
        <div className="flex items-center justify-between px-4 py-2 border-b border-[var(--ao-border)] bg-[var(--ao-surface)]">
          <div className="flex items-center gap-2 text-sm font-medium">
            <Terminal size={16} />
            <span>Logs — {agentName}</span>
            <span className={`inline-block w-2 h-2 rounded-full ${connected ? 'bg-green-400' : 'bg-red-400'}`} />
            {error && <span className="text-xs text-red-400 ml-2">{error}</span>}
          </div>
          <div className="flex items-center gap-1">
            <button
              onClick={() => setPaused(!paused)}
              className="p-1.5 rounded hover:bg-[var(--ao-surface-hover)] text-[var(--ao-text-muted)]"
              title={paused ? 'Resume auto-scroll' : 'Pause auto-scroll'}
            >
              {paused ? <Play size={14} /> : <Pause size={14} />}
            </button>
            <button
              onClick={clearLogs}
              className="p-1.5 rounded hover:bg-[var(--ao-surface-hover)] text-[var(--ao-text-muted)]"
              title="Clear logs"
            >
              <Trash2 size={14} />
            </button>
            <button
              onClick={onClose}
              className="p-1.5 rounded hover:bg-[var(--ao-surface-hover)] text-[var(--ao-text-muted)]"
              title="Close"
            >
              <X size={14} />
            </button>
          </div>
        </div>

        {/* Log body */}
        <div className="flex-1 overflow-y-auto p-3 font-mono text-xs leading-5 bg-[#0d1117]">
          {logs.length === 0 ? (
            <div className="text-[var(--ao-text-muted)] text-center pt-8">
              Waiting for log output...
            </div>
          ) : (
            logs.map((entry, i) => (
              <div key={i} className="flex gap-2 hover:bg-white/5">
                <span className="text-[#484f58] select-none shrink-0">
                  {new Date(entry.timestamp).toLocaleTimeString()}
                </span>
                <span className={`shrink-0 w-12 text-right ${entry.stream === 'stderr' ? 'text-yellow-500' : 'text-blue-400'}`}>
                  {entry.stream}
                </span>
                <span className="text-[#c9d1d9] whitespace-pre-wrap break-all">{entry.line}</span>
              </div>
            ))
          )}
          <div ref={logsEndRef} />
        </div>

        {/* Footer */}
        <div className="flex items-center justify-between px-4 py-1.5 border-t border-[var(--ao-border)] bg-[var(--ao-surface)] text-xs text-[var(--ao-text-muted)]">
          <span>{logs.length} lines</span>
          {paused && <span className="text-yellow-400">⏸ Auto-scroll paused</span>}
        </div>
      </div>
    </div>
  );
}
