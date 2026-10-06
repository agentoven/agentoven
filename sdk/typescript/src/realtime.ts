/**
 * Realtime voice sessions with an AgentOven agent (Pro).
 *
 * A session is one WebSocket carrying JSON text frames. Audio is PCM16,
 * 24 kHz, mono, base64 inside the frames — the same on every client.
 *
 * ```ts
 * const call = await connectRealtime({ url, agent: 'support-agent', apiKey, voice: 'marin' });
 * call.sendAudio(pcmChunk);                       // stream the microphone
 * for await (const ev of call) {
 *   if (ev.type === 'audio') speaker.write(ev.audio);   // Uint8Array of PCM16
 *   if (ev.type === 'transcript' && ev.final) console.log(ev.role, ev.text);
 * }
 * ```
 *
 * The agent always runs on its own provider, which must offer the `realtime`
 * modality.
 *
 * Browsers cannot set headers on a WebSocket. A backend that holds the API key
 * mints a short-lived token (`ProClient.realtimeToken`) and hands it to the
 * page, which connects with `connectRealtime({ url, agent, token })`: no
 * headers, the token offered as the `agentoven-token.<token>` WebSocket
 * subprotocol next to `agentoven.v1`.
 *
 * If the agent's input guardrails block what the caller said, the server sends
 * an `error` event and closes the socket with code 1008; iterating the session
 * then rejects with a `RealtimeError` carrying the reason.
 */

import { fromBase64, toBase64 } from './bytes.js';

export const REALTIME_AUDIO_FORMAT = 'pcm16-24khz-mono';

/** The WebSocket subprotocol the server selects. */
export const REALTIME_SUBPROTOCOL = 'agentoven.v1';
/** Offered next to {@link REALTIME_SUBPROTOCOL}; the rest of the string is the token. */
export const REALTIME_TOKEN_SUBPROTOCOL_PREFIX = 'agentoven-token.';
/** Close code the server uses when it ends a call for policy (input guardrails). */
export const REALTIME_CLOSE_POLICY = 1008;

// RFC 6455: a subprotocol is an HTTP token — no spaces, separators or controls.
const SUBPROTOCOL_TOKEN = /^[!#$%&'*+\-.^_`|~0-9A-Za-z]+$/;

export type RealtimeEventType =
  | 'ready'
  | 'audio'
  | 'transcript'
  | 'tool_call'
  | 'tool_result'
  | 'turn_done'
  | 'interrupted' // the caller spoke over the agent: stop and discard queued playback
  | 'error';

/** One frame from the server. Only the fields relevant to `type` are set. */
export interface RealtimeEvent {
  type: RealtimeEventType;
  sessionId?: string;
  format?: string;
  /** Raw PCM16 bytes (decoded from the wire's base64). */
  audio?: Uint8Array;
  role?: 'user' | 'assistant';
  text?: string;
  final?: boolean;
  toolName?: string;
  toolArgs?: string;
  toolOutput?: string;
  isError?: boolean;
  error?: string;
}

/** The slice of the WebSocket API a session uses. */
export interface WebSocketLike {
  send(data: string): void;
  close(code?: number, reason?: string): void;
  addEventListener(type: 'message', fn: (ev: { data: unknown }) => void): void;
  addEventListener(type: 'close', fn: (ev: { code?: number; reason?: string }) => void): void;
  addEventListener(type: 'error', fn: (ev: unknown) => void): void;
  addEventListener(type: 'open', fn: () => void): void;
}

/**
 * Opens a socket. `protocols` is set (and `headers` empty) for a token
 * connection: pass it as the second argument of `new WebSocket(url, protocols)`,
 * the only form a browser allows. Otherwise `headers` carry the credentials.
 */
export type WebSocketFactory = (url: string, headers: Record<string, string>, protocols?: string[]) => WebSocketLike;

export interface RealtimeOptions {
  /** Control plane base URL, e.g. https://agentoven.example.com */
  url: string;
  agent: string;
  apiKey?: string;
  kitchen?: string;
  /**
   * A short-lived token from `ProClient.realtimeToken`. When set, the socket is
   * opened the browser way — `new WebSocket(url, ['agentoven.v1',
   * 'agentoven-token.<token>'])`, no headers — and `apiKey` / `kitchen` are not
   * sent (the token is bound to the agent and kitchen).
   */
  token?: string;
  voice?: string;
  model?: string;
  /**
   * Supply a WebSocket constructor wrapper. The default uses the global
   * WebSocket: with a token, the browser-compatible `(url, protocols)` form;
   * otherwise Node's `headers` option (verified on Node 24), which is how the
   * API key and kitchen reach the server.
   */
  webSocket?: WebSocketFactory;
}

/** The WebSocket URL for an agent's realtime endpoint. */
export function realtimeUrl(base: string, agent: string, voice?: string, model?: string): string {
  let b = base.replace(/\/+$/, '');
  if (b.startsWith('https://')) b = 'wss://' + b.slice('https://'.length);
  else if (b.startsWith('http://')) b = 'ws://' + b.slice('http://'.length);
  const q = new URLSearchParams();
  if (voice) q.set('voice', voice);
  if (model) q.set('model', model);
  const qs = q.toString();
  return `${b}/api/v1/realtime/${encodeURIComponent(agent)}${qs ? `?${qs}` : ''}`;
}

export class RealtimeError extends Error {
  constructor(message: string) {
    super(message);
    this.name = 'RealtimeError';
  }
}

function parseFrame(raw: unknown): RealtimeEvent | null {
  if (typeof raw !== 'string') return null; // the protocol is text frames only
  const f = JSON.parse(raw) as Record<string, unknown>;
  const ev: RealtimeEvent = { type: f.type as RealtimeEventType };
  if (f.session_id) ev.sessionId = f.session_id as string;
  if (f.format) ev.format = f.format as string;
  if (f.audio) ev.audio = fromBase64(f.audio as string);
  if (f.role) ev.role = f.role as 'user' | 'assistant';
  if (f.text !== undefined) ev.text = f.text as string;
  if (f.final) ev.final = true;
  if (f.tool_name) ev.toolName = f.tool_name as string;
  if (f.tool_args) ev.toolArgs = f.tool_args as string;
  if (f.tool_output !== undefined) ev.toolOutput = f.tool_output as string;
  if (f.is_error) ev.isError = true;
  if (f.error) ev.error = f.error as string;
  return ev;
}

/** An open realtime call. Iterate it with `for await` to receive events. */
export class RealtimeSession implements AsyncIterable<RealtimeEvent> {
  readonly sessionId: string;
  readonly audioFormat: string;
  /** Set once the server has closed the call. */
  closeCode?: number;
  closeReason?: string;

  private lastError = '';
  private queue: RealtimeEvent[] = [];
  private waiter: { resolve: (r: IteratorResult<RealtimeEvent>) => void; reject: (e: Error) => void } | null = null;
  private ended = false;
  private failure: Error | null = null;

  /** @internal use connectRealtime */
  constructor(private readonly ws: WebSocketLike, ready: RealtimeEvent) {
    this.sessionId = ready.sessionId ?? '';
    this.audioFormat = ready.format ?? REALTIME_AUDIO_FORMAT;
  }

  /** @internal */
  _push(ev: RealtimeEvent): void {
    if (ev.type === 'error' && ev.error) this.lastError = ev.error;
    if (this.waiter) {
      const w = this.waiter;
      this.waiter = null;
      w.resolve({ value: ev, done: false });
    } else {
      this.queue.push(ev);
    }
  }

  /** @internal the socket closed: record why, and fail the iteration on a policy close. */
  _closed(code?: number, reason?: string): void {
    this.closeCode = code;
    this.closeReason = reason;
    if (code === REALTIME_CLOSE_POLICY) {
      const why = this.lastError || reason || "blocked by the agent's guardrails";
      this._end(new RealtimeError(`realtime call ended by the server (close code 1008): ${why}`));
    } else {
      this._end();
    }
  }

  /** @internal */
  _end(err?: Error): void {
    this.ended = true;
    if (err) this.failure = err;
    if (this.waiter) {
      const w = this.waiter;
      this.waiter = null;
      if (err) w.reject(err);
      else w.resolve({ value: undefined, done: true });
    }
  }

  private sendFrame(frame: Record<string, unknown>): void {
    this.ws.send(JSON.stringify(frame));
  }

  /** Stream a chunk of the caller's speech (PCM16, 24 kHz, mono). */
  sendAudio(pcm: Uint8Array | ArrayBuffer): void {
    const bytes = pcm instanceof Uint8Array ? pcm : new Uint8Array(pcm);
    this.sendFrame({ type: 'audio', audio: toBase64(bytes) });
  }

  /** End the caller's turn. Only needed when you do your own turn detection. */
  commit(): void {
    this.sendFrame({ type: 'commit' });
  }

  /** Send a typed turn instead of speech. */
  sendText(text: string): void {
    this.sendFrame({ type: 'text', text });
  }

  /** Stop the agent mid-response (barge-in). */
  interrupt(): void {
    this.sendFrame({ type: 'interrupt' });
  }

  close(): void {
    this.ws.close(1000);
  }

  [Symbol.asyncIterator](): AsyncIterator<RealtimeEvent> {
    return {
      next: (): Promise<IteratorResult<RealtimeEvent>> => {
        const ev = this.queue.shift();
        if (ev) return Promise.resolve({ value: ev, done: false });
        if (this.ended) {
          return this.failure ? Promise.reject(this.failure) : Promise.resolve({ value: undefined, done: true });
        }
        return new Promise((resolve, reject) => {
          this.waiter = { resolve, reject };
        });
      },
      return: (): Promise<IteratorResult<RealtimeEvent>> => {
        this.close();
        return Promise.resolve({ value: undefined, done: true });
      },
    };
  }
}

const defaultFactory: WebSocketFactory = (url, headers, protocols) => {
  const WS = (globalThis as { WebSocket?: new (u: string, o?: unknown) => WebSocketLike }).WebSocket;
  if (!WS) {
    throw new RealtimeError('no global WebSocket: use a browser or a Node with a global WebSocket (24 is verified), or pass options.webSocket');
  }
  // The (url, protocols) form is the one browsers allow; headers are Node-only.
  return protocols ? new WS(url, protocols) : new WS(url, { headers });
};

/**
 * Open a realtime session. Resolves once the server sends `ready`; rejects if
 * the upgrade is refused or the first frame is not `ready`.
 */
export function connectRealtime(opts: RealtimeOptions): Promise<RealtimeSession> {
  const headers: Record<string, string> = {};
  let protocols: string[] | undefined;
  if (opts.token) {
    const proto = REALTIME_TOKEN_SUBPROTOCOL_PREFIX + opts.token;
    if (!SUBPROTOCOL_TOKEN.test(proto)) {
      return Promise.reject(new RealtimeError('realtime token contains characters that are not valid in a WebSocket subprotocol'));
    }
    protocols = [REALTIME_SUBPROTOCOL, proto];
  } else {
    if (opts.apiKey) headers['Authorization'] = `Bearer ${opts.apiKey}`;
    if (opts.kitchen) {
      headers['X-Kitchen'] = opts.kitchen;
      headers['X-Kitchen-Id'] = opts.kitchen;
    }
  }
  const url = realtimeUrl(opts.url, opts.agent, opts.voice, opts.model);
  const factory = opts.webSocket ?? defaultFactory;

  return new Promise<RealtimeSession>((resolve, reject) => {
    const ws = protocols ? factory(url, headers, protocols) : factory(url, headers);
    let session: RealtimeSession | null = null;

    ws.addEventListener('message', (m) => {
      let ev: RealtimeEvent | null;
      try {
        ev = parseFrame(m.data);
      } catch {
        return;
      }
      if (!ev) return;
      if (!session) {
        if (ev.type !== 'ready') {
          // Reject first: closing fires the close handler synchronously, which
          // would otherwise reject with a less useful message and hide the
          // server's own error text.
          reject(new RealtimeError(`expected a ready frame, got '${ev.type}'${ev.error ? `: ${ev.error}` : ''}`));
          ws.close(1002);
          return;
        }
        session = new RealtimeSession(ws, ev);
        resolve(session);
        return;
      }
      session._push(ev);
    });
    ws.addEventListener('close', (c) => {
      if (session) session._closed(c.code, c.reason);
      else {
        const why = c.reason ? `: ${c.reason}` : '';
        reject(new RealtimeError(`realtime session closed before ready${c.code ? ` (code ${c.code}${why})` : ''}`));
      }
    });
    ws.addEventListener('error', () => {
      if (!session) reject(new RealtimeError('could not open realtime session (connection refused or upgrade rejected)'));
    });
  });
}
