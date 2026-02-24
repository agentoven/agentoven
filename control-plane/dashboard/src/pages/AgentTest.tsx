import { useState, useRef, useEffect } from 'react';
import { useParams, useNavigate } from 'react-router-dom';
import {
  ArrowLeft, Send, Bot, User, Clock, Coins, Hash, Brain, Wrench,
  ChevronDown, ChevronRight, Zap, AlertCircle, CheckCircle2,
} from 'lucide-react';
import {
  agents,
  type TestAgentResponse,
  type InvokeAgentResponse,
  type ExecutionTurn,
  type ThinkingBlock,
  type TokenUsage,
} from '../api';
import { useAPI } from '../hooks';
import {
  Card, Spinner, ErrorBanner, Button, StatusBadge,
} from '../components/UI';

// ── Types ─────────────────────────────────────────────────────

type TestMode = 'simple' | 'agentic';

interface ChatMessage {
  role: 'user' | 'assistant';
  content: string;
  mode: TestMode;
  meta?: {
    provider?: string;
    model?: string;
    latency_ms: number;
    usage: TokenUsage;
    trace_id: string;
    thinking_blocks?: ThinkingBlock[];
    // Agentic mode — full turn-by-turn execution trace
    turns?: ExecutionTurn[];
    total_turns?: number;
  };
}

// ── Main Page ─────────────────────────────────────────────────

export function AgentTestPage() {
  const { name } = useParams<{ name: string }>();
  const navigate = useNavigate();
  const { data: agent, loading, error } = useAPI(() => agents.get(name!));
  const [messages, setMessages] = useState<ChatMessage[]>([]);
  const [input, setInput] = useState('');
  const [sending, setSending] = useState(false);
  const [sendError, setSendError] = useState<string | null>(null);
  const [mode, setMode] = useState<TestMode>('simple');
  const [thinkingEnabled, setThinkingEnabled] = useState(false);
  const bottomRef = useRef<HTMLDivElement>(null);

  useEffect(() => {
    bottomRef.current?.scrollIntoView({ behavior: 'smooth' });
  }, [messages]);

  const send = async () => {
    if (!input.trim() || sending || !name) return;
    const userMsg = input.trim();
    setInput('');
    setSendError(null);
    setMessages((prev) => [...prev, { role: 'user', content: userMsg, mode }]);
    setSending(true);

    try {
      if (mode === 'simple') {
        const res: TestAgentResponse = await agents.test(name, userMsg, thinkingEnabled);
        setMessages((prev) => [
          ...prev,
          {
            role: 'assistant',
            content: res.response,
            mode: 'simple',
            meta: {
              provider: res.provider,
              model: res.model,
              latency_ms: res.latency_ms,
              usage: res.usage,
              trace_id: res.trace_id,
              thinking_blocks: res.thinking_blocks,
            },
          },
        ]);
      } else {
        const res: InvokeAgentResponse = await agents.invoke(name, userMsg, undefined, thinkingEnabled);
        setMessages((prev) => [
          ...prev,
          {
            role: 'assistant',
            content: res.response,
            mode: 'agentic',
            meta: {
              latency_ms: res.latency_ms,
              usage: res.usage,
              trace_id: res.trace_id,
              turns: res.execution_trace?.turns,
              total_turns: res.turns,
            },
          },
        ]);
      }
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
          <span>{messages.filter((m) => m.role === 'assistant').length} responses</span>
        </div>
      </div>

      {/* Messages */}
      <div className="flex-1 overflow-y-auto p-8 space-y-4">
        {messages.length === 0 && (
          <div className="text-center text-[var(--ao-text-muted)] py-16">
            <Bot size={48} className="mx-auto mb-3 opacity-40" />
            <p className="text-sm">Send a message to test <strong>{agent.name}</strong></p>
            <p className="text-xs mt-1">
              {mode === 'simple'
                ? `One-shot — routed through ${agent.model_provider}/${agent.model_name}`
                : `Agentic — full multi-turn loop with tool calling (max ${agent.max_turns || 10} turns)`}
            </p>
          </div>
        )}

        {messages.map((msg, i) => (
          <div key={i}>
            {msg.role === 'user' ? (
              <UserBubble content={msg.content} />
            ) : msg.mode === 'agentic' && msg.meta?.turns ? (
              <AgenticResponse msg={msg} />
            ) : (
              <SimpleResponse msg={msg} />
            )}
          </div>
        ))}

        {sending && (
          <div className="flex gap-3">
            <div className="w-7 h-7 rounded-full bg-[var(--ao-brand)]/20 flex items-center justify-center flex-shrink-0">
              <Bot size={14} className="text-[var(--ao-brand-light)]" />
            </div>
            <Card className="!p-3">
              <div className="flex items-center gap-2">
                <div className="flex gap-1">
                  <span className="w-2 h-2 bg-[var(--ao-text-muted)] rounded-full animate-bounce" style={{ animationDelay: '0ms' }} />
                  <span className="w-2 h-2 bg-[var(--ao-text-muted)] rounded-full animate-bounce" style={{ animationDelay: '150ms' }} />
                  <span className="w-2 h-2 bg-[var(--ao-text-muted)] rounded-full animate-bounce" style={{ animationDelay: '300ms' }} />
                </div>
                {mode === 'agentic' && <span className="text-xs text-[var(--ao-text-muted)]">Running agentic loop…</span>}
                {thinkingEnabled && <span className="text-xs text-purple-400 flex items-center gap-1"><Brain size={10} /> Thinking…</span>}
              </div>
            </Card>
          </div>
        )}
        {sendError && <ErrorBanner message={sendError} />}
        <div ref={bottomRef} />
      </div>

      {/* Input */}
      <div className="border-t border-[var(--ao-border)] px-8 py-4">
        {/* Controls */}
        <div className="flex items-center gap-3 mb-3">
          {/* Mode toggle */}
          <div className="flex rounded-lg border border-[var(--ao-border)] overflow-hidden text-xs">
            <button
              type="button"
              onClick={() => setMode('simple')}
              className={`px-3 py-1.5 transition-colors ${mode === 'simple' ? 'bg-[var(--ao-brand)] text-white' : 'text-[var(--ao-text-muted)] hover:bg-[var(--ao-surface-hover)]'}`}
            >
              <Zap size={12} className="inline mr-1" />Simple
            </button>
            <button
              type="button"
              onClick={() => setMode('agentic')}
              className={`px-3 py-1.5 transition-colors ${mode === 'agentic' ? 'bg-[var(--ao-brand)] text-white' : 'text-[var(--ao-text-muted)] hover:bg-[var(--ao-surface-hover)]'}`}
            >
              <Bot size={12} className="inline mr-1" />Agentic
            </button>
          </div>

          {/* Thinking toggle */}
          <label className="flex items-center gap-2 text-xs cursor-pointer select-none">
            <div className="relative">
              <input
                type="checkbox"
                checked={thinkingEnabled}
                onChange={(e) => setThinkingEnabled(e.target.checked)}
                className="sr-only"
              />
              <div className={`w-8 h-4 rounded-full transition-colors ${thinkingEnabled ? 'bg-purple-500' : 'bg-slate-600'}`}>
                <div className={`w-3 h-3 rounded-full bg-white absolute top-0.5 transition-transform ${thinkingEnabled ? 'translate-x-4' : 'translate-x-0.5'}`} />
              </div>
            </div>
            <Brain size={12} className={thinkingEnabled ? 'text-purple-400' : 'text-[var(--ao-text-muted)]'} />
            <span className={thinkingEnabled ? 'text-purple-400' : 'text-[var(--ao-text-muted)]'}>
              Extended Thinking
            </span>
          </label>

          {mode === 'agentic' && (
            <span className="text-xs text-[var(--ao-text-muted)]">
              Max {agent.max_turns || 10} turns • Uses resolved ingredients
            </span>
          )}
        </div>

        <div className="flex gap-3 items-end">
          <textarea
            value={input}
            onChange={(e) => setInput(e.target.value)}
            onKeyDown={handleKeyDown}
            rows={1}
            placeholder={
              mode === 'simple'
                ? 'Type a message... (Enter to send)'
                : 'Describe a task for the agent... (Enter to send)'
            }
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

// ── User Bubble ───────────────────────────────────────────────

function UserBubble({ content }: { content: string }) {
  return (
    <div className="flex gap-3 justify-end">
      <div className="max-w-[70%]">
        <Card className="!p-3 bg-[var(--ao-brand)]/10 border-[var(--ao-brand)]/30">
          <p className="text-sm whitespace-pre-wrap">{content}</p>
        </Card>
      </div>
      <div className="w-7 h-7 rounded-full bg-slate-600 flex items-center justify-center flex-shrink-0 mt-1">
        <User size={14} className="text-slate-300" />
      </div>
    </div>
  );
}

// ── Simple Response (one-shot) ────────────────────────────────

function SimpleResponse({ msg }: { msg: ChatMessage }) {
  const [showThinking, setShowThinking] = useState(false);
  const hasThinking = msg.meta?.thinking_blocks && msg.meta.thinking_blocks.length > 0;

  return (
    <div className="flex gap-3">
      <div className="w-7 h-7 rounded-full bg-[var(--ao-brand)]/20 flex items-center justify-center flex-shrink-0 mt-1">
        <Bot size={14} className="text-[var(--ao-brand-light)]" />
      </div>
      <div className="max-w-[70%]">
        {/* Thinking blocks */}
        {hasThinking && (
          <button
            type="button"
            onClick={() => setShowThinking(!showThinking)}
            className="flex items-center gap-1 text-xs text-purple-400 mb-2 hover:text-purple-300"
          >
            {showThinking ? <ChevronDown size={12} /> : <ChevronRight size={12} />}
            <Brain size={12} /> Thinking ({msg.meta!.thinking_blocks!.reduce((s, b) => s + b.token_count, 0)} tokens)
          </button>
        )}
        {showThinking && msg.meta?.thinking_blocks?.map((block, i) => (
          <div key={i} className="mb-2 p-3 rounded-lg bg-purple-500/10 border border-purple-500/20 text-xs font-mono whitespace-pre-wrap text-purple-300 max-h-48 overflow-y-auto">
            {block.content}
          </div>
        ))}

        <Card className="!p-3">
          <p className="text-sm whitespace-pre-wrap">{msg.content}</p>
        </Card>
        {msg.meta && (
          <div className="flex flex-wrap gap-3 mt-1.5 text-[10px] text-[var(--ao-text-muted)]">
            {msg.meta.provider && <span>{msg.meta.provider}/{msg.meta.model}</span>}
            <span className="flex items-center gap-0.5">
              <Clock size={10} /> {msg.meta.latency_ms}ms
            </span>
            <span>{msg.meta.usage.input_tokens}→{msg.meta.usage.output_tokens} tok</span>
            {msg.meta.usage.thinking_tokens ? (
              <span className="text-purple-400">🧠 {msg.meta.usage.thinking_tokens} thinking</span>
            ) : null}
            <span>${msg.meta.usage.estimated_cost_usd.toFixed(6)}</span>
            <span className="font-mono">{msg.meta.trace_id.substring(0, 8)}…</span>
          </div>
        )}
      </div>
    </div>
  );
}

// ── Agentic Response (multi-turn with timeline) ───────────────

function AgenticResponse({ msg }: { msg: ChatMessage }) {
  const [expanded, setExpanded] = useState(true);
  const turns = msg.meta?.turns ?? [];

  return (
    <div className="flex gap-3">
      <div className="w-7 h-7 rounded-full bg-emerald-500/20 flex items-center justify-center flex-shrink-0 mt-1">
        <Bot size={14} className="text-emerald-400" />
      </div>
      <div className="flex-1 max-w-[85%]">
        {/* Summary badge */}
        <div className="flex items-center gap-2 mb-2">
          <span className="px-2 py-0.5 text-xs rounded-full bg-emerald-500/20 text-emerald-400 font-medium">
            Agentic • {msg.meta?.total_turns} turn{(msg.meta?.total_turns ?? 0) > 1 ? 's' : ''}
          </span>
          <button
            type="button"
            onClick={() => setExpanded(!expanded)}
            className="text-xs text-[var(--ao-text-muted)] hover:text-[var(--ao-text)] flex items-center gap-1"
          >
            {expanded ? <ChevronDown size={12} /> : <ChevronRight size={12} />}
            {expanded ? 'Hide' : 'Show'} execution trace
          </button>
        </div>

        {/* Turn timeline */}
        {expanded && turns.length > 0 && (
          <div className="mb-3 space-y-2">
            {turns.map((turn) => (
              <TurnCard key={turn.number} turn={turn} />
            ))}
          </div>
        )}

        {/* Final response */}
        <Card className="!p-3 border-emerald-500/20">
          <p className="text-sm whitespace-pre-wrap">{msg.content}</p>
        </Card>

        {msg.meta && (
          <div className="flex flex-wrap gap-3 mt-1.5 text-[10px] text-[var(--ao-text-muted)]">
            <span className="flex items-center gap-0.5">
              <Clock size={10} /> {msg.meta.latency_ms}ms total
            </span>
            <span>{msg.meta.usage.input_tokens}→{msg.meta.usage.output_tokens} tok</span>
            {msg.meta.usage.thinking_tokens ? (
              <span className="text-purple-400">🧠 {msg.meta.usage.thinking_tokens} thinking</span>
            ) : null}
            <span>${msg.meta.usage.estimated_cost_usd.toFixed(6)}</span>
            <span className="font-mono">{msg.meta.trace_id.substring(0, 8)}…</span>
          </div>
        )}
      </div>
    </div>
  );
}

// ── Individual Turn Card ──────────────────────────────────────

function TurnCard({ turn }: { turn: ExecutionTurn }) {
  const [showThinking, setShowThinking] = useState(false);
  const hasToolCalls = turn.tool_calls && turn.tool_calls.length > 0;
  const hasThinking = turn.thinking_blocks && turn.thinking_blocks.length > 0;
  const isTextResponse = !!turn.response && !hasToolCalls;

  return (
    <div className="relative pl-6 before:absolute before:left-2 before:top-0 before:bottom-0 before:w-px before:bg-[var(--ao-border)]">
      {/* Timeline dot */}
      <div className="absolute left-0 top-1.5 w-5 h-5 rounded-full bg-[var(--ao-surface)] border-2 border-[var(--ao-border)] flex items-center justify-center z-10">
        {hasToolCalls ? (
          <Wrench size={10} className="text-amber-400" />
        ) : isTextResponse ? (
          <CheckCircle2 size={10} className="text-emerald-400" />
        ) : (
          <Zap size={10} className="text-blue-400" />
        )}
      </div>

      <div className="p-3 rounded-lg border border-[var(--ao-border)] bg-[var(--ao-surface)]">
        {/* Turn header */}
        <div className="flex items-center justify-between mb-2">
          <span className="text-xs font-medium text-[var(--ao-text-muted)]">
            Turn {turn.number}
          </span>
          <div className="flex items-center gap-2 text-[10px] text-[var(--ao-text-muted)]">
            <span>{turn.latency_ms}ms</span>
            <span>{turn.usage.total_tokens} tok</span>
          </div>
        </div>

        {/* Thinking */}
        {hasThinking && (
          <>
            <button
              type="button"
              onClick={() => setShowThinking(!showThinking)}
              className="flex items-center gap-1 text-[10px] text-purple-400 mb-2 hover:text-purple-300"
            >
              {showThinking ? <ChevronDown size={10} /> : <ChevronRight size={10} />}
              <Brain size={10} /> Thinking ({turn.thinking_blocks!.reduce((s, b) => s + b.token_count, 0)} tokens)
            </button>
            {showThinking && turn.thinking_blocks!.map((block, i) => (
              <div key={i} className="mb-2 p-2 rounded bg-purple-500/10 border border-purple-500/20 text-[10px] font-mono whitespace-pre-wrap text-purple-300 max-h-32 overflow-y-auto">
                {block.content}
              </div>
            ))}
          </>
        )}

        {/* Tool calls */}
        {hasToolCalls && (
          <div className="space-y-2">
            {turn.tool_calls!.map((tc, i) => (
              <div key={tc.id || i}>
                <div className="flex items-center gap-2 mb-1">
                  <Wrench size={10} className="text-amber-400" />
                  <span className="text-xs font-medium text-amber-400">{tc.name}</span>
                </div>
                <div className="p-2 rounded bg-amber-500/5 border border-amber-500/20 text-[10px] font-mono text-[var(--ao-text-muted)] max-h-24 overflow-y-auto">
                  {JSON.stringify(tc.arguments, null, 2)}
                </div>
                {/* Corresponding result */}
                {turn.tool_results && turn.tool_results[i] && (
                  <div className={`mt-1 p-2 rounded text-[10px] font-mono max-h-24 overflow-y-auto ${
                    turn.tool_results[i].is_error
                      ? 'bg-red-500/10 border border-red-500/20 text-red-400'
                      : 'bg-emerald-500/5 border border-emerald-500/20 text-emerald-300'
                  }`}>
                    {turn.tool_results[i].is_error && <AlertCircle size={10} className="inline mr-1" />}
                    {turn.tool_results[i].content}
                  </div>
                )}
              </div>
            ))}
          </div>
        )}

        {/* Text response (final turn) */}
        {isTextResponse && (
          <p className="text-xs text-[var(--ao-text-muted)] line-clamp-3">{turn.response}</p>
        )}
      </div>
    </div>
  );
}
