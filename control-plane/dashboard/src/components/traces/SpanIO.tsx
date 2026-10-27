import { useState, type ReactNode } from 'react';
import { Copy, Check } from 'lucide-react';
import type { ThinkingBlock } from '../../api';

/** Compact, readable JSON for anything that is not a conversation. */
export function formatJSON(value: unknown): string {
  if (value == null) return '';
  if (typeof value === 'string') return value;
  try { return JSON.stringify(value, null, 2); } catch { return String(value); }
}

export function CopyButton({ text, label = 'Copy' }: { text: string; label?: string }) {
  const [done, setDone] = useState(false);
  return (
    <button
      onClick={(e) => { e.stopPropagation(); navigator.clipboard?.writeText(text).then(() => { setDone(true); setTimeout(() => setDone(false), 1200); }); }}
      className="inline-flex items-center gap-1 text-[0.7rem] text-[var(--ao-text-muted)] hover:text-[var(--ao-text)]"
    >
      {done ? <Check size={12} /> : <Copy size={12} />} {done ? 'Copied' : label}
    </button>
  );
}

/** The model's reasoning, one collapsible block per piece. */
export function ThinkingPanel({ blocks, tokens }: { blocks: ThinkingBlock[]; tokens?: number }) {
  if (blocks.length === 0) return null;
  return (
    <>
      <div className="text-[0.7rem] text-[var(--ao-text-muted)] uppercase tracking-wider font-semibold mt-5 mb-2">
        Thinking ({blocks.length} {blocks.length === 1 ? 'block' : 'blocks'}{tokens ? `, ${tokens} tokens` : ''})
      </div>
      {blocks.map((b, i) => (
        <details key={i} className="mb-2 rounded-md border border-[var(--ao-border)] bg-[var(--ao-bg)]" open={blocks.length === 1}>
          <summary className="cursor-pointer px-3 py-2 text-sm text-[var(--ao-text-muted)]">
            {b.redacted ? 'Withheld by the provider' : `Block ${i + 1}`}{b.token_count ? ` · ${b.token_count} tokens` : ''}
          </summary>
          <pre className="text-sm whitespace-pre-wrap break-words px-3 pb-3 max-h-[400px] overflow-auto">{b.content}</pre>
        </details>
      ))}
    </>
  );
}

interface ToolCall { id?: string; name?: string; arguments?: unknown; function?: { name?: string; arguments?: unknown } }
interface Message {
  role?: string; content?: unknown; content_parts?: { type?: string; text?: string }[];
  tool_calls?: ToolCall[]; tool_call_id?: string; name?: string;
}

const roleStyle: Record<string, string> = {
  system: 'bg-slate-500/15 border-slate-500/30',
  user: 'bg-blue-500/10 border-blue-500/30',
  assistant: 'bg-violet-500/10 border-violet-500/30',
  tool: 'bg-amber-500/10 border-amber-500/30',
};

function textOf(m: Message): string {
  if (typeof m.content === 'string') return m.content;
  if (Array.isArray(m.content_parts)) return m.content_parts.map((p) => p.text ?? `[${p.type ?? 'part'}]`).join('\n');
  return m.content == null ? '' : formatJSON(m.content);
}

function ToolCalls({ calls }: { calls: ToolCall[] }) {
  return (
    <div className="mt-2 flex flex-col gap-1.5">
      {calls.map((c, i) => {
        const name = c.name ?? c.function?.name ?? 'tool';
        const args = c.arguments ?? c.function?.arguments;
        let shown = typeof args === 'string' ? args : formatJSON(args);
        if (typeof args === 'string') { try { shown = JSON.stringify(JSON.parse(args), null, 2); } catch { /* keep as is */ } }
        return (
          <div key={c.id ?? i} className="rounded border border-amber-500/30 bg-amber-500/5 px-2.5 py-1.5">
            <div className="text-xs font-semibold text-amber-400">🔧 {name}</div>
            {shown && <pre className="text-xs whitespace-pre-wrap break-words mt-1 max-h-40 overflow-auto">{shown}</pre>}
          </div>
        );
      })}
    </div>
  );
}

function Messages({ messages }: { messages: Message[] }) {
  return (
    <div className="flex flex-col gap-2">
      {messages.map((m, i) => {
        const role = m.role ?? 'message';
        const text = textOf(m);
        return (
          <div key={i} className={`rounded-lg border px-3 py-2 ${roleStyle[role] ?? 'bg-[var(--ao-bg)] border-[var(--ao-border)]'}`}>
            <div className="flex items-center justify-between mb-1">
              <span className="text-[0.65rem] uppercase tracking-wider font-semibold text-[var(--ao-text-muted)]">
                {role}{m.name ? ` · ${m.name}` : ''}{m.tool_call_id ? ` · ${m.tool_call_id.slice(0, 10)}` : ''}
              </span>
              {text && <CopyButton text={text} />}
            </div>
            {text && <div className="text-sm whitespace-pre-wrap break-words max-h-72 overflow-auto">{text}</div>}
            {m.tool_calls && m.tool_calls.length > 0 && <ToolCalls calls={m.tool_calls} />}
          </div>
        );
      })}
    </div>
  );
}

function isMessages(v: unknown): v is Message[] {
  return Array.isArray(v) && v.length > 0 && v.every((m) => m && typeof m === 'object' && 'role' in (m as object));
}

/**
 * A span's input or output, read the way a person wants it: a conversation as messages, a model's answer
 * as text with its thinking and tool calls, anything else as JSON. "Raw" shows the stored JSON.
 */
export function SpanIO({ value, empty }: { value: unknown; empty: string }) {
  const [raw, setRaw] = useState(false);
  if (value == null || value === '') return <div className="text-center py-8 text-[var(--ao-text-muted)] text-sm">{empty}</div>;

  const out = value as { content?: unknown; thinking_blocks?: ThinkingBlock[]; tool_calls?: ToolCall[]; finish_reason?: string };
  const isAnswer = !Array.isArray(value) && typeof value === 'object' && ('content' in (value as object) || 'tool_calls' in (value as object));

  let body: ReactNode;
  if (raw) {
    body = <pre className="text-sm whitespace-pre-wrap break-words p-3 bg-[var(--ao-bg)] rounded-md border border-[var(--ao-border)] max-h-[560px] overflow-auto">{formatJSON(value)}</pre>;
  } else if (isMessages(value)) {
    body = <Messages messages={value} />;
  } else if (isAnswer) {
    const text = typeof out.content === 'string' ? out.content : formatJSON(out.content);
    body = (
      <div>
        {text && (
          <div className="rounded-lg border border-violet-500/30 bg-violet-500/10 px-3 py-2">
            <div className="flex items-center justify-between mb-1">
              <span className="text-[0.65rem] uppercase tracking-wider font-semibold text-[var(--ao-text-muted)]">
                assistant{out.finish_reason ? ` · ${out.finish_reason}` : ''}
              </span>
              <CopyButton text={text} />
            </div>
            <div className="text-sm whitespace-pre-wrap break-words max-h-96 overflow-auto">{text}</div>
          </div>
        )}
        {out.tool_calls && out.tool_calls.length > 0 && <ToolCalls calls={out.tool_calls} />}
        <ThinkingPanel blocks={out.thinking_blocks ?? []} />
      </div>
    );
  } else {
    body = <pre className="text-sm whitespace-pre-wrap break-words p-3 bg-[var(--ao-bg)] rounded-md border border-[var(--ao-border)] max-h-[560px] overflow-auto">{formatJSON(value)}</pre>;
  }

  return (
    <div>
      <div className="flex justify-end gap-3 mb-2">
        <button onClick={() => setRaw(!raw)} className="text-[0.7rem] text-[var(--ao-text-muted)] hover:text-[var(--ao-text)]">{raw ? 'Formatted' : 'Raw JSON'}</button>
        <CopyButton text={formatJSON(value)} label="Copy JSON" />
      </div>
      {body}
    </div>
  );
}
