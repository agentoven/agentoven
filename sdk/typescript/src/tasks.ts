/**
 * Background tasks (`/api/v1/tasks`): agent work that outlives the request that started it, under its
 * own spend cap and with a durable journal.
 *
 * ```ts
 * const tasks = new Tasks({ url: 'https://agentoven.example.com', apiKey: '...', kitchen: 'research' });
 * const task = await tasks.start('researcher', 'find why checkout latency doubled', { maxCostUsd: 5 });
 * for await (const event of tasks.events(task.id)) console.log(event.seq, event.type);
 * const done = await tasks.wait(task.id);
 * if (done.status === 'paused' && done.pause_kind === 'budget') {
 *   await tasks.resume(task.id, { extend: { add_cost_usd: 5, reason: 'close to done' } });
 * }
 * ```
 *
 * A task stops by itself when it finishes, when it reaches its budget (a person extends it or ends
 * it), when it needs a tool approved, or when it keeps repeating itself. On a Pro server a decision
 * on a paused task is signed: pass `password` (signed for the gate the pause waits at) or
 * `signingToken` (one you hold).
 */

import { AgentOvenAPIError } from './pro-client.js';

export interface TasksOptions {
  url: string;
  apiKey?: string;
  kitchen?: string;
  /** Replaces `fetch` (tests, proxies). */
  fetch?: typeof fetch;
  /** How long one request may take, in milliseconds (default 120 000). */
  timeoutMs?: number;
}

export type TaskStatus =
  | 'queued' | 'running' | 'paused' | 'completed' | 'max_turns' | 'failed' | 'cancelled'
  | 'budget_exhausted' | 'interrupted' | 'abandoned' | 'stalled';
export type PauseKind = 'budget' | 'approval' | 'stall';

export interface TaskUsage {
  input_tokens?: number;
  output_tokens?: number;
  total_tokens?: number;
  estimated_cost?: number;
}

export interface BudgetLimits {
  max_cost_usd?: number;
  max_tokens?: number;
  max_active_seconds?: number;
  max_turns?: number;
}

/** A background task as the control plane reports it. */
export interface Task {
  id: string;
  agent: string;
  kitchen: string;
  status: TaskStatus;
  /** What a paused task is waiting for. */
  pause_kind?: PauseKind;
  budget_pause?: { dimension: string; spent: number; cap: number; scope?: string; turn?: number };
  pending?: { turn: number; gated_names: string[] };
  stall?: { tool: string; count: number; limit: number };
  budget?: { limits?: BudgetLimits; spent_cost_usd?: number; spent_tokens?: number };
  turns: number;
  usage: TaskUsage;
  /** The answer, once there is one. Withheld (`result_blocked`) if the agent's output guardrails refuse it. */
  result?: string;
  result_blocked?: boolean;
  error?: string;
  attempts: number;
  /** The replica running the task, when several control-plane replicas share tasks. */
  owner?: string;
  metadata?: Record<string, string>;
  created_at: string;
  updated_at: string;
}

export interface StartTaskOptions {
  sessionId?: string;
  /** Caps. They only ever tighten what the agent and the server already set. */
  maxCostUsd?: number;
  maxTokens?: number;
  maxActiveSeconds?: number;
  maxTurns?: number;
  metadata?: Record<string, string>;
}

export interface StartedTask {
  id: string;
  status: TaskStatus;
  agent: string;
  links: { self: string; events: string; cancel: string; resume: string };
  budget?: BudgetLimits;
}

export interface BudgetExtension {
  add_cost_usd?: number;
  add_tokens?: number;
  add_active_seconds?: number;
  add_turns?: number;
  reason?: string;
  /** Makes the extension apply once however often it is sent. Chosen for you when omitted. */
  id?: string;
}

export interface ResumeTaskOptions {
  /** The decision: required at a tool approval or a stall; implied (approved) by `extend`. */
  approved?: boolean;
  note?: string;
  /** Raise a budget pause's caps. Needs the `budget:extend` permission. */
  extend?: BudgetExtension;
  password?: string;
  signingToken?: string;
}

/** One entry of a task's event stream. */
export interface TaskEvent {
  seq: number;
  time?: string;
  type: string;
  event: Record<string, any>;
}

export interface WaitOptions {
  timeoutMs?: number;
  pollMs?: number;
}

/** The signed gate each kind of pause is decided at. */
export const TASK_PAUSE_GATES: Record<PauseKind, string> = {
  approval: 'tool_approval',
  budget: 'budget_extension',
  stall: 'resume_guidance',
};

const EXTEND_KEYS = ['add_cost_usd', 'add_tokens', 'add_active_seconds', 'add_turns'] as const;
const SIGNING_TOKEN_HEADER = 'X-Signing-Token';

const seg = (s: string) => encodeURIComponent(s);

/** Server-sent events from a stream of text chunks. Comment lines (`: ping`) are skipped. */
export async function* parseSse(chunks: AsyncIterable<string>): AsyncGenerator<{ id: number | null; event: string; data: string }> {
  let buf = '';
  let cur = { id: null as number | null, event: '', data: '' };
  let hasData = false;
  for await (const chunk of chunks) {
    buf += chunk;
    let i: number;
    while ((i = buf.indexOf('\n')) >= 0) {
      const line = buf.slice(0, i).replace(/\r$/, '');
      buf = buf.slice(i + 1);
      if (line === '') {
        if (hasData || cur.event) yield cur;
        cur = { id: null, event: '', data: '' };
        hasData = false;
      } else if (line.startsWith(':')) {
        continue;
      } else if (line.startsWith('id:')) {
        const n = Number.parseInt(line.slice(3).trim(), 10);
        cur.id = Number.isNaN(n) ? null : n;
      } else if (line.startsWith('event:')) {
        cur.event = line.slice(6).trim();
      } else if (line.startsWith('data:')) {
        const v = line.slice(5).replace(/^ /, '');
        cur.data = hasData ? `${cur.data}\n${v}` : v;
        hasData = true;
      }
    }
  }
  if (hasData) yield cur;
}

export class Tasks {
  private readonly baseUrl: string;
  private readonly apiKey?: string;
  private readonly kitchen: string;
  private readonly fetchFn: typeof fetch;
  private readonly timeoutMs: number;

  constructor(opts: TasksOptions) {
    this.timeoutMs = opts.timeoutMs ?? 120_000;
    this.baseUrl = opts.url.replace(/\/+$/, '');
    this.apiKey = opts.apiKey;
    this.kitchen = opts.kitchen ?? 'default';
    this.fetchFn = opts.fetch ?? fetch;
  }

  /** Start `agent` as a background task. Returns at once. A dollar cap on a model with no known price is refused (422); a full queue answers 429. */
  start(agent: string, message: string, opts: StartTaskOptions = {}): Promise<StartedTask> {
    if (!message.trim()) throw new Error('a task needs a message');
    const budget: Record<string, number> = {};
    for (const [key, value] of [
      ['max_cost_usd', opts.maxCostUsd], ['max_tokens', opts.maxTokens],
      ['max_active_seconds', opts.maxActiveSeconds], ['max_turns', opts.maxTurns],
    ] as const) {
      if (value === undefined) continue;
      if (!(value > 0)) throw new Error(`${key} must be more than zero`);
      budget[key] = value;
    }
    const body: Record<string, unknown> = { agent, message };
    if (opts.sessionId) body.session_id = opts.sessionId;
    if (Object.keys(budget).length) body.budget = budget;
    if (opts.metadata && Object.keys(opts.metadata).length) body.metadata = opts.metadata;
    return this.request('POST', '/api/v1/tasks', body);
  }

  /** The kitchen's tasks that are running, paused or unfinished. `status` narrows. */
  async list(status?: TaskStatus): Promise<Task[]> {
    const reply = await this.request<{ tasks?: Task[] }>('GET', `/api/v1/tasks${status ? `?status=${encodeURIComponent(status)}` : ''}`);
    return reply.tasks ?? [];
  }

  /** One task. 404 for a task of another kitchen. */
  get(taskId: string): Promise<Task> {
    return this.request('GET', `/api/v1/tasks/${seg(taskId)}`);
  }

  /** Stop a task. It cannot be resumed afterwards. */
  cancel(taskId: string): Promise<Task> {
    return this.request('POST', `/api/v1/tasks/${seg(taskId)}/cancel`, {});
  }

  /**
   * Continue a task that is paused or was interrupted. An interrupted task needs no decision. A paused one does:
   * `extend` at a budget pause, `approved` (with `note`) at a tool approval or a stall.
   */
  async resume(taskId: string, opts: ResumeTaskOptions = {}): Promise<Task> {
    if (opts.password && opts.signingToken) throw new Error('pass password or signingToken, not both');
    let extend: Record<string, unknown> | undefined;
    if (opts.extend) {
      extend = Object.fromEntries(Object.entries(opts.extend).filter(([, v]) => v !== undefined && v !== 0));
      if (!EXTEND_KEYS.some((k) => k in extend!)) throw new Error('an extension adds something: add_cost_usd, add_tokens, add_active_seconds or add_turns');
      for (const k of EXTEND_KEYS) if (k in extend && !((extend[k] as number) > 0)) throw new Error(`${k} must be more than zero`);
      extend.id ??= crypto.randomUUID();
    }
    let body: Record<string, unknown> | undefined;
    if (opts.approved !== undefined || opts.note !== undefined || extend) {
      if (opts.approved === false && extend) throw new Error('a denial does not extend a budget');
      body = { approved: opts.approved ?? true };
      if (opts.note !== undefined) body.note = opts.note;
      if (extend) body.extend = extend;
    }
    const path = `/api/v1/tasks/${seg(taskId)}/resume`;
    if ((opts.password || opts.signingToken) && body) {
      let token = opts.signingToken;
      if (opts.password) {
        const kind = (await this.get(taskId)).pause_kind;
        const gate = kind ? TASK_PAUSE_GATES[kind] : undefined;
        if (!gate) throw new Error(`task ${taskId} is not waiting for a decision, so there is nothing to sign`);
        const payload: Record<string, unknown> = {};
        if (body.note !== undefined) payload.note = body.note;
        if (body.extend !== undefined) payload.extend = body.extend;
        token = await this.sign(gate, taskId, body.approved ? 'approved' : 'rejected', payload, opts.password);
      }
      return this.request('POST', path, body, { [SIGNING_TOKEN_HEADER]: token! });
    }
    return this.request('POST', path, body ?? {});
  }

  /** Poll until the task is no longer queued or running: finished, or stopped for a person (`paused`, `interrupted`). */
  async wait(taskId: string, opts: WaitOptions = {}): Promise<Task> {
    const pollMs = opts.pollMs ?? 2000;
    const deadline = opts.timeoutMs === undefined ? Infinity : Date.now() + opts.timeoutMs;
    for (;;) {
      const task = await this.get(taskId);
      if (task.status !== 'queued' && task.status !== 'running') return task;
      if (Date.now() + pollMs > deadline) throw new Error(`task ${taskId} is still ${task.status} after ${opts.timeoutMs}ms`);
      await new Promise((r) => setTimeout(r, pollMs));
    }
  }

  /**
   * Follow a task's events as they happen, ending when the task does. `after` is the last sequence number you
   * saw: a dropped connection carries on from there. Events are kept in memory on the replica that runs the task;
   * for one that is not running there the stream is its current status.
   */
  async *events(taskId: string, after = 0): AsyncGenerator<TaskEvent> {
    const res = await this.fetchFn(`${this.baseUrl}/api/v1/tasks/${seg(taskId)}/events${after ? `?after=${Math.trunc(after)}` : ''}`, {
      headers: { ...this.headers(), Accept: 'text/event-stream' },
    });
    if (!res.ok) throw await this.failure(res);
    if (!res.body) return;
    const decoder = new TextDecoder();
    async function* text(body: ReadableStream<Uint8Array>): AsyncGenerator<string> {
      const reader = body.getReader();
      try {
        for (;;) {
          const { done, value } = await reader.read();
          if (done) return;
          yield decoder.decode(value, { stream: true });
        }
      } finally {
        reader.releaseLock();
      }
    }
    for await (const sse of parseSse(text(res.body))) {
      let payload: { seq?: number; time?: string; event?: Record<string, any> };
      try {
        payload = JSON.parse(sse.data);
      } catch {
        continue;
      }
      const event = payload.event ?? {};
      yield { seq: sse.id ?? payload.seq ?? 0, time: payload.time, type: event.type ?? sse.event, event };
    }
  }

  // ── internals ──────────────────────────────────────────────

  private headers(extra?: Record<string, string>): Record<string, string> {
    const h: Record<string, string> = { 'Content-Type': 'application/json', 'X-Kitchen': this.kitchen, 'X-Kitchen-Id': this.kitchen, ...extra };
    if (this.apiKey) h['Authorization'] = `Bearer ${this.apiKey}`;
    return h;
  }

  private async failure(res: Response): Promise<AgentOvenAPIError> {
    const raw = await res.text();
    let detail: unknown = raw;
    try {
      detail = JSON.parse(raw);
    } catch {
      /* keep the text */
    }
    return new AgentOvenAPIError(res.status, detail);
  }

  private async request<T>(method: string, path: string, body?: unknown, extra?: Record<string, string>): Promise<T> {
    const res = await this.fetchFn(`${this.baseUrl}${path}`, {
      method,
      headers: this.headers(extra),
      body: body !== undefined ? JSON.stringify(body) : undefined,
      signal: AbortSignal.timeout(this.timeoutMs),
    });
    if (!res.ok) throw await this.failure(res);
    const text = await res.text();
    return text ? (JSON.parse(text) as T) : ({} as T);
  }

  /** The two halves of signing a decision with a local account's password (`ProClient.sign` does the same). */
  private async sign(gate: string, recordId: string, meaning: 'approved' | 'rejected', payload: Record<string, unknown>, password: string): Promise<string> {
    const req = { gate, record_id: recordId, meaning, payload };
    const challenge = await this.request<{ subject: { digest: string }; reauth?: string }>('POST', '/api/v1/signatures/challenge', req);
    if (challenge.reauth === 'oidc') {
      throw new AgentOvenAPIError(400, { error: 'use_oidc', message: 'this account signs through the identity provider: sign in the dashboard, or use a local account (password) to sign from the SDK' });
    }
    const reply = await this.request<{ signing_token: string }>('POST', '/api/v1/signatures/token', { ...req, digest: challenge.subject.digest, password });
    return reply.signing_token;
  }
}
