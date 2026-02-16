import { useState, useRef, useEffect } from 'react';
import { useParams, useNavigate } from 'react-router-dom';
import { ArrowLeft, Send, Bot, User, Clock, Coins, Hash } from 'lucide-react';
import { agents, type TestAgentResponse } from '../api';
import { useAPI } from '../hooks';
import {
  Card, Spinner, ErrorBanner, Button, StatusBadge,
} from '../components/UI';

interface ChatMessage {
  role: 'user' | 'assistant';
  content: string;
  meta?: {
    provider: string;
    model: string;
    latency_ms: number;
    usage: {
      input_tokens: number;
      output_tokens: number;
      total_tokens: number;
      estimated_cost_usd: number;
    };
    trace_id: string;
  };
}

export function AgentTestPage() {
  const { name } = useParams<{ name: string }>();
  const navigate = useNavigate();
  const { data: agent, loading, error } = useAPI(() => agents.get(name!));
  const [messages, setMessages] = useState<ChatMessage[]>([]);
  const [input, setInput] = useState('');
  const [sending, setSending] = useState(false);
  const [sendError, setSendError] = useState<string | null>(null);
  const bottomRef = useRef<HTMLDivElement>(null);

  useEffect(() => {
    bottomRef.current?.scrollIntoView({ behavior: 'smooth' });
  }, [messages]);

  const send = async () => {
    if (!input.trim() || sending || !name) return;
    const userMsg = input.trim();
    setInput('');
    setSendError(null);
    setMessages((prev) => [...prev, { role: 'user', content: userMsg }]);
    setSending(true);

    try {
      const res: TestAgentResponse = await agents.test(name, userMsg);
      setMessages((prev) => [
        ...prev,
        {
          role: 'assistant',
          content: res.response,
          meta: {
            provider: res.provider,
            model: res.model,
            latency_ms: res.latency_ms,
            usage: res.usage,
            trace_id: res.trace_id,
          },
        },
      ]);
    } catch (e) {
      setSendError(e instanceof Error ? e.message : 'Test failed');
    }
    setSending(false);
  };

  const handleKeyDown = (e: React.KeyboardEvent) => {
    if (e.key === 'Enter' && !e.shiftKey) {
      e.preventDefault();
      send();
    }
  };

  // Total stats
  const totalTokens = messages.reduce((sum, m) => sum + (m.meta?.usage.total_tokens ?? 0), 0);
  const totalCost = messages.reduce((sum, m) => sum + (m.meta?.usage.estimated_cost_usd ?? 0), 0);

  if (loading) return <Spinner />;
  if (error) return <ErrorBanner message={error} />;
  if (!agent) return <ErrorBanner message="Agent not found" />;

  return (
    <div className="flex flex-col h-full">
      {/* Header */}
      <div className="flex items-center justify-between px-8 py-4 border-b border-[var(--ao-border)]">
        <div className="flex items-center gap-3">
          <button
            onClick={() => navigate('/agents')}
            className="text-[var(--ao-text-muted)] hover:text-[var(--ao-text)]"
          >
            <ArrowLeft size={18} />
          </button>
          <Bot size={20} className="text-[var(--ao-brand-light)]" />
          <div>
            <h1 className="text-lg font-bold">Test: {agent.name}</h1>
            <p className="text-xs text-[var(--ao-text-muted)]">
              {agent.model_provider} / {agent.model_name}
            </p>
          </div>
          <StatusBadge status={agent.status} />
        </div>
        <div className="flex items-center gap-4 text-xs text-[var(--ao-text-muted)]">
          <span className="flex items-center gap-1"><Hash size={12} /> {totalTokens} tokens</span>
          <span className="flex items-center gap-1"><Coins size={12} /> ${totalCost.toFixed(6)}</span>
          <span>{messages.filter((m) => m.role === 'assistant').length} turns</span>
        </div>
      </div>

      {/* Messages */}
      <div className="flex-1 overflow-y-auto p-8 space-y-4">
        {messages.length === 0 && (
          <div className="text-center text-[var(--ao-text-muted)] py-16">
            <Bot size={48} className="mx-auto mb-3 opacity-40" />
            <p className="text-sm">Send a message to test <strong>{agent.name}</strong></p>
            <p className="text-xs mt-1">Messages are routed through {agent.model_provider}/{agent.model_name}</p>
          </div>
        )}
        {messages.map((msg, i) => (
          <div key={i} className={`flex gap-3 ${msg.role === 'user' ? 'justify-end' : ''}`}>
            {msg.role === 'assistant' && (
              <div className="w-7 h-7 rounded-full bg-[var(--ao-brand)]/20 flex items-center justify-center flex-shrink-0 mt-1">
                <Bot size={14} className="text-[var(--ao-brand-light)]" />
              </div>
            )}
            <div className={`max-w-[70%] ${msg.role === 'user' ? 'order-first' : ''}`}>
              <Card className={`!p-3 ${msg.role === 'user' ? 'bg-[var(--ao-brand)]/10 border-[var(--ao-brand)]/30' : ''}`}>
                <p className="text-sm whitespace-pre-wrap">{msg.content}</p>
              </Card>
              {msg.meta && (
                <div className="flex flex-wrap gap-3 mt-1.5 text-[10px] text-[var(--ao-text-muted)]">
                  <span className="flex items-center gap-0.5">
                    <Clock size={10} /> {msg.meta.latency_ms}ms
                  </span>
                  <span>{msg.meta.usage.input_tokens}→{msg.meta.usage.output_tokens} tok</span>
                  <span>${msg.meta.usage.estimated_cost_usd.toFixed(6)}</span>
                  <span className="font-mono">{msg.meta.trace_id.substring(0, 8)}…</span>
                </div>
              )}
            </div>
            {msg.role === 'user' && (
              <div className="w-7 h-7 rounded-full bg-slate-600 flex items-center justify-center flex-shrink-0 mt-1">
                <User size={14} className="text-slate-300" />
              </div>
            )}
          </div>
        ))}
        {sending && (
          <div className="flex gap-3">
            <div className="w-7 h-7 rounded-full bg-[var(--ao-brand)]/20 flex items-center justify-center flex-shrink-0">
              <Bot size={14} className="text-[var(--ao-brand-light)]" />
            </div>
            <Card className="!p-3">
              <div className="flex gap-1">
                <span className="w-2 h-2 bg-[var(--ao-text-muted)] rounded-full animate-bounce" style={{ animationDelay: '0ms' }} />
                <span className="w-2 h-2 bg-[var(--ao-text-muted)] rounded-full animate-bounce" style={{ animationDelay: '150ms' }} />
                <span className="w-2 h-2 bg-[var(--ao-text-muted)] rounded-full animate-bounce" style={{ animationDelay: '300ms' }} />
              </div>
            </Card>
          </div>
        )}
        {sendError && <ErrorBanner message={sendError} />}
        <div ref={bottomRef} />
      </div>

      {/* Input */}
      <div className="border-t border-[var(--ao-border)] px-8 py-4">
        <div className="flex gap-3 items-end">
          <textarea
            value={input}
            onChange={(e) => setInput(e.target.value)}
            onKeyDown={handleKeyDown}
            rows={1}
            placeholder="Type a message... (Enter to send, Shift+Enter for newline)"
            className="flex-1 px-4 py-3 rounded-xl bg-[var(--ao-surface)] border border-[var(--ao-border)] text-sm outline-none focus:border-[var(--ao-brand)] resize-none"
            disabled={sending || agent.status !== 'ready'}
          />
          <Button
            onClick={send}
            disabled={!input.trim() || sending || agent.status !== 'ready'}
          >
            <Send size={16} />
          </Button>
        </div>
        {agent.status !== 'ready' && (
          <p className="text-xs text-amber-400 mt-2">Agent must be in "ready" status to test. Current: {agent.status}</p>
        )}
      </div>
    </div>
  );
}
