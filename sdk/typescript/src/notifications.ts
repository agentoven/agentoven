/**
 * Listen to a live-feed notification channel.
 *
 * A channel of kind `websocket` pushes every notification sent through it, as one JSON message each, to the
 * connections listening at `/api/v1/channels/{name}/ws`. A recipe's `notify` step sends through it, and so do
 * the platform's own events (an approval waiting, a step failing) for channels that subscribe to them.
 *
 * ```ts
 * await pro.createChannel({ name: 'ops' });                       // once
 *
 * const feed = await listenToChannel({ url, channel: 'ops', apiKey });
 * for await (const message of feed) console.log(message.payload.message);
 * ```
 *
 * Nothing is kept: a listener sees what is sent after it connects. The first frame, `ready`, is read for you.
 *
 * Browsers cannot set headers on a WebSocket. A backend that holds the API key asks for a ticket
 * (`ProClient.channelTicket`), good for a minute and for that one channel, and the page connects with
 * `listenToChannel({ url, channel, token })`: no headers, the ticket offered as the `agentoven-token.<ticket>`
 * subprotocol next to `agentoven.v1`.
 */

import type { WebSocketFactory, WebSocketLike } from './realtime.js';

export const CHANNEL_SUBPROTOCOL = 'agentoven.v1';
export const CHANNEL_TICKET_SUBPROTOCOL_PREFIX = 'agentoven-token.';

// RFC 6455: a subprotocol is an HTTP token — no spaces, separators or controls.
const SUBPROTOCOL_TOKEN = /^[!#$%&'*+\-.^_`|~0-9A-Za-z]+$/;

/** One notification, as the server sends it. */
export interface Notification {
  type: string;
  run_id: string;
  recipe_name?: string;
  step_name?: string;
  kitchen: string;
  /** For a notify step: `message`, and `subject` and any `payload` fields it was given. */
  payload?: Record<string, unknown> & { message?: string; subject?: string };
  timestamp: string;
}

export class NotificationError extends Error {
  constructor(message: string) {
    super(message);
    this.name = 'NotificationError';
  }
}

export interface ListenOptions {
  /** Control plane base URL, e.g. https://agentoven.example.com */
  url: string;
  channel: string;
  apiKey?: string;
  kitchen?: string;
  /** A ticket from `ProClient.channelTicket`. When set, no headers are sent (the ticket is bound to the channel and kitchen). */
  token?: string;
  /** Supply a WebSocket constructor wrapper. The default uses the global WebSocket. */
  webSocket?: WebSocketFactory;
}

/** The WebSocket address of a channel. */
export function channelUrl(base: string, channel: string): string {
  let b = base.replace(/\/+$/, '');
  if (b.startsWith('https://')) b = 'wss://' + b.slice('https://'.length);
  else if (b.startsWith('http://')) b = 'ws://' + b.slice('http://'.length);
  return `${b}/api/v1/channels/${encodeURIComponent(channel)}/ws`;
}

const defaultFactory: WebSocketFactory = (url, headers, protocols) => {
  const WS = (globalThis as { WebSocket?: new (u: string, o?: unknown) => WebSocketLike }).WebSocket;
  if (!WS) throw new NotificationError('no global WebSocket: use a browser or a recent Node, or pass options.webSocket');
  return protocols ? new WS(url, protocols) : new WS(url, { headers });
};

/** An open connection to a channel. Iterate it for notifications; the iteration ends when the connection does. */
export class NotificationListener implements AsyncIterable<Notification> {
  private queue: Notification[] = [];
  private waiting: (() => void) | null = null;
  private ended = false;
  private failure: Error | null = null;

  /** @internal */
  constructor(private readonly ws: WebSocketLike, readonly ready: Record<string, unknown>) {
    ws.addEventListener('message', (ev) => {
      if (typeof ev.data !== 'string') return;
      const frame = JSON.parse(ev.data) as Notification;
      // `ready` is the handshake, already read. Some WebSocket implementations hand a listener added during
      // an event that very event, so it is skipped here rather than shown as a notification.
      if (frame.type === 'ready') return;
      this.queue.push(frame);
      this.wake();
    });
    ws.addEventListener('close', (ev) => {
      const code = ev.code ?? 1000;
      if (code !== 1000 && code !== 1001 && code !== 1005) {
        this.failure = new NotificationError(`the listener was closed (code ${code}${ev.reason ? `: ${ev.reason}` : ''})`);
      }
      this.ended = true;
      this.wake();
    });
    ws.addEventListener('error', () => {
      this.ended = true;
      this.wake();
    });
  }

  private wake(): void {
    this.waiting?.();
    this.waiting = null;
  }

  close(): void {
    this.ws.close(1000);
  }

  async *[Symbol.asyncIterator](): AsyncIterator<Notification> {
    for (;;) {
      const next = this.queue.shift();
      if (next) {
        yield next;
        continue;
      }
      if (this.ended) {
        if (this.failure) throw this.failure;
        return;
      }
      await new Promise<void>((resolve) => { this.waiting = resolve; });
    }
  }
}

/** Connect to a channel. Resolves once the server says it is ready; rejects if the connection is refused. */
export function listenToChannel(opts: ListenOptions): Promise<NotificationListener> {
  const headers: Record<string, string> = {};
  let protocols: string[] | undefined;
  if (opts.token) {
    const proto = CHANNEL_TICKET_SUBPROTOCOL_PREFIX + opts.token;
    if (!SUBPROTOCOL_TOKEN.test(proto)) return Promise.reject(new NotificationError('the ticket contains characters that are not valid in a WebSocket subprotocol'));
    protocols = [CHANNEL_SUBPROTOCOL, proto];
  } else {
    if (opts.apiKey) headers['Authorization'] = `Bearer ${opts.apiKey}`;
    if (opts.kitchen) headers['X-Kitchen'] = opts.kitchen;
    protocols = undefined;
  }
  const ws = (opts.webSocket ?? defaultFactory)(channelUrl(opts.url, opts.channel), headers, protocols);
  return new Promise((resolve, reject) => {
    let opened = false;
    ws.addEventListener('message', (ev) => {
      if (opened || typeof ev.data !== 'string') return;
      opened = true;
      const first = JSON.parse(ev.data) as Record<string, unknown>;
      if (first.type !== 'ready') {
        ws.close();
        reject(new NotificationError(`expected a ready frame, got ${JSON.stringify(first.type)}`));
        return;
      }
      resolve(new NotificationListener(ws, first));
    });
    ws.addEventListener('close', (ev) => {
      if (!opened) reject(new NotificationError(`the channel closed the connection before it was ready${ev.code ? ` (code ${ev.code}${ev.reason ? `: ${ev.reason}` : ''})` : ''}`));
    });
    ws.addEventListener('error', () => {
      if (!opened) reject(new NotificationError('could not connect to the channel (it may not exist, may not be a websocket channel, or the key may be wrong)'));
    });
  });
}
