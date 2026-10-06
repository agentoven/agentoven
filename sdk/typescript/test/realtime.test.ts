import { describe, expect, it } from 'vitest';
import { ProClient } from '../src/pro-client.js';
import { connectRealtime, realtimeUrl, RealtimeError, type WebSocketLike } from '../src/realtime.js';

/** A scriptable stand-in for a WebSocket. */
class FakeSocket implements WebSocketLike {
  sent: string[] = [];
  closedWith: number | undefined;
  private fns: Record<string, Array<(e: any) => void>> = {};
  addEventListener(type: string, fn: (e: any) => void): void {
    (this.fns[type] ??= []).push(fn);
  }
  send(data: string): void {
    this.sent.push(data);
  }
  close(code?: number, reason?: string): void {
    this.closedWith = code;
    this.emit('close', { code: code ?? 1000, reason });
  }
  emit(type: string, e: any = {}): void {
    for (const fn of this.fns[type] ?? []) fn(e);
  }
  frame(obj: unknown): void {
    this.emit('message', { data: JSON.stringify(obj) });
  }
}

function open() {
  const sock = new FakeSocket();
  let seen: { url: string; headers: Record<string, string>; protocols?: string[] } | undefined;
  const webSocket = (url: string, headers: Record<string, string>, protocols?: string[]) => {
    seen = { url, headers, protocols };
    return sock;
  };
  return { sock, webSocket, seen: () => seen! };
}

const b64 = (bytes: number[]) => btoa(String.fromCharCode(...bytes));

describe('realtimeUrl', () => {
  it('maps the scheme and escapes the agent', () => {
    expect(realtimeUrl('http://localhost:8080', 'voice')).toBe('ws://localhost:8080/api/v1/realtime/voice');
    expect(realtimeUrl('https://ao.example.com/', 'my agent', 'marin', 'gpt-realtime')).toBe(
      'wss://ao.example.com/api/v1/realtime/my%20agent?voice=marin&model=gpt-realtime',
    );
  });
});

describe('connectRealtime', () => {
  it('sends credentials and resolves on ready', async () => {
    const { sock, webSocket, seen } = open();
    const p = connectRealtime({ url: 'http://h', agent: 'voice', apiKey: 'ao_key', kitchen: 'payments', voice: 'marin', webSocket });
    sock.frame({ type: 'ready', session_id: 's1', format: 'pcm16-24khz-mono' });
    const call = await p;

    expect(seen().url).toBe('ws://h/api/v1/realtime/voice?voice=marin');
    expect(seen().headers).toMatchObject({ Authorization: 'Bearer ao_key', 'X-Kitchen': 'payments' });
    expect(call.sessionId).toBe('s1');
    expect(call.audioFormat).toBe('pcm16-24khz-mono');
  });

  it('rejects when the first frame is not ready', async () => {
    const { sock, webSocket } = open();
    const p = connectRealtime({ url: 'http://h', agent: 'voice', webSocket });
    sock.frame({ type: 'error', error: 'provider down' });
    await expect(p).rejects.toThrow(/provider down/);
  });

  it('rejects when the socket errors before ready (refused upgrade)', async () => {
    const { sock, webSocket } = open();
    const p = connectRealtime({ url: 'http://h', agent: 'voice', webSocket });
    sock.emit('error');
    await expect(p).rejects.toBeInstanceOf(RealtimeError);
  });

  it('rejects when the socket closes before ready', async () => {
    const { sock, webSocket } = open();
    const p = connectRealtime({ url: 'http://h', agent: 'voice', webSocket });
    sock.emit('close', { code: 1006 });
    await expect(p).rejects.toThrow(/closed before ready.*1006/);
  });
});

describe('RealtimeSession', () => {
  async function ready() {
    const o = open();
    const p = connectRealtime({ url: 'http://h', agent: 'voice', webSocket: o.webSocket });
    o.sock.frame({ type: 'ready', session_id: 's1', format: 'pcm16-24khz-mono' });
    return { ...o, call: await p };
  }

  it('encodes outgoing frames per the wire protocol', async () => {
    const { sock, call } = await ready();
    call.sendAudio(new Uint8Array([0, 1, 2]));
    call.sendText('hello');
    call.commit();
    call.interrupt();
    expect(sock.sent.map((s) => JSON.parse(s))).toEqual([
      { type: 'audio', audio: b64([0, 1, 2]) },
      { type: 'text', text: 'hello' },
      { type: 'commit' },
      { type: 'interrupt' },
    ]);
  });

  it('delivers events in order with audio decoded to bytes', async () => {
    const { sock, call } = await ready();
    sock.frame({ type: 'transcript', role: 'user', text: 'hi', final: true });
    sock.frame({ type: 'tool_call', tool_name: 'lookup_order', tool_args: '{}' });
    sock.frame({ type: 'tool_result', tool_name: 'lookup_order', tool_output: 'shipped' });
    sock.frame({ type: 'audio', audio: b64([1, 2]) });
    sock.frame({ type: 'turn_done' });
    sock.close(1000);

    const events = [];
    for await (const ev of call) events.push(ev);

    expect(events.map((e) => e.type)).toEqual(['transcript', 'tool_call', 'tool_result', 'audio', 'turn_done']);
    expect(events[0]).toMatchObject({ role: 'user', text: 'hi', final: true });
    expect(events[1]).toMatchObject({ toolName: 'lookup_order' });
    expect(events[2]).toMatchObject({ toolOutput: 'shipped' });
    expect(Array.from(events[3].audio!)).toEqual([1, 2]);
  });

  it('surfaces barge-in as an interrupted event', async () => {
    const { sock, call } = await ready();
    sock.frame({ type: 'audio', audio: b64([1]) });
    sock.frame({ type: 'interrupted' });
    sock.close(1000);
    const types: string[] = [];
    for await (const ev of call) types.push(ev.type);
    expect(types).toEqual(['audio', 'interrupted']);
  });

  it('wakes a waiting consumer when an event arrives later', async () => {
    const { sock, call } = await ready();
    const it = call[Symbol.asyncIterator]();
    const pending = it.next();
    sock.frame({ type: 'turn_done' });
    expect((await pending).value).toMatchObject({ type: 'turn_done' });
  });

  it('ends the iteration when the socket closes, and close() sends a normal closure', async () => {
    const { sock, call } = await ready();
    const it = call[Symbol.asyncIterator]();
    const pending = it.next();
    call.close();
    expect((await pending).done).toBe(true);
    expect(sock.closedWith).toBe(1000);
  });

  it('breaking out of for-await closes the call', async () => {
    const { sock, call } = await ready();
    sock.frame({ type: 'turn_done' });
    for await (const _ of call) break;
    expect(sock.closedWith).toBe(1000);
  });
});

describe('ProClient.realtime', () => {
  it('uses the client url, key and kitchen', async () => {
    const o = open();
    const pro = new ProClient({ url: 'https://ao.example.com', apiKey: 'k', kitchen: 'acme' });
    const p = pro.realtime('voice', { voice: 'marin', webSocket: o.webSocket });
    o.sock.frame({ type: 'ready', session_id: 's' });
    await p;
    expect(o.seen().url).toBe('wss://ao.example.com/api/v1/realtime/voice?voice=marin');
    expect(o.seen().headers).toMatchObject({ Authorization: 'Bearer k', 'X-Kitchen': 'acme' });
  });
});

describe('browser-style token auth', () => {
  it('opens with the token subprotocols and no headers', async () => {
    const { sock, webSocket, seen } = open();
    const p = connectRealtime({ url: 'https://h', agent: 'voice', apiKey: 'ao_key', kitchen: 'payments', token: 'tok_abc.123', voice: 'marin', webSocket });
    sock.frame({ type: 'ready', session_id: 's1' });
    await p;
    expect(seen().url).toBe('wss://h/api/v1/realtime/voice?voice=marin');
    expect(seen().protocols).toEqual(['agentoven.v1', 'agentoven-token.tok_abc.123']);
    // the key and kitchen are on the options but must not go on the wire
    expect(seen().headers).toEqual({});
  });

  it('header auth passes no protocols', async () => {
    const { sock, webSocket, seen } = open();
    const p = connectRealtime({ url: 'http://h', agent: 'voice', apiKey: 'k', webSocket });
    sock.frame({ type: 'ready', session_id: 's1' });
    await p;
    expect(seen().protocols).toBeUndefined();
    expect(seen().headers).toMatchObject({ Authorization: 'Bearer k' });
  });

  it('refuses a token that cannot be a subprotocol', async () => {
    const { webSocket } = open();
    await expect(connectRealtime({ url: 'http://h', agent: 'v', token: 'has space', webSocket })).rejects.toThrow(/subprotocol/);
    await expect(connectRealtime({ url: 'http://h', agent: 'v', token: 'a,b', webSocket })).rejects.toBeInstanceOf(RealtimeError);
  });

  it('the default factory uses the browser (url, protocols) form for a token and { headers } otherwise', async () => {
    const calls: unknown[][] = [];
    class StubWS extends FakeSocket {
      constructor(...args: unknown[]) {
        super();
        calls.push(args);
        queueMicrotask(() => this.frame({ type: 'ready', session_id: 's' }));
      }
    }
    const g = globalThis as { WebSocket?: unknown };
    const saved = g.WebSocket;
    g.WebSocket = StubWS;
    try {
      await connectRealtime({ url: 'http://h', agent: 'v', token: 'tok' });
      await connectRealtime({ url: 'http://h', agent: 'v', apiKey: 'k', kitchen: 'acme' });
    } finally {
      g.WebSocket = saved;
    }
    expect(calls[0]).toEqual(['ws://h/api/v1/realtime/v', ['agentoven.v1', 'agentoven-token.tok']]);
    expect(calls[1][0]).toBe('ws://h/api/v1/realtime/v');
    expect(calls[1][1]).toMatchObject({ headers: { Authorization: 'Bearer k', 'X-Kitchen': 'acme' } });
  });

  it('ProClient.realtime forwards the token and sends no headers', async () => {
    const o = open();
    const pro = new ProClient({ url: 'https://ao.example.com', apiKey: 'k', kitchen: 'acme' });
    const p = pro.realtime('voice', { token: 'tok', webSocket: o.webSocket });
    o.sock.frame({ type: 'ready', session_id: 's' });
    await p;
    expect(o.seen().protocols).toEqual(['agentoven.v1', 'agentoven-token.tok']);
    expect(o.seen().headers).toEqual({});
  });
});

describe('guardrail close (1008)', () => {
  async function ready() {
    const o = open();
    const p = connectRealtime({ url: 'http://h', agent: 'voice', webSocket: o.webSocket });
    o.sock.frame({ type: 'ready', session_id: 's1' });
    return { ...o, call: await p };
  }

  it('delivers the error event, then rejects with the reason', async () => {
    const { sock, call } = await ready();
    sock.frame({ type: 'error', error: 'input blocked by guardrail pii' });
    sock.close(1008, 'policy');
    const events: string[] = [];
    const run = async () => {
      for await (const ev of call) events.push(`${ev.type}:${ev.error}`);
    };
    await expect(run()).rejects.toThrow(/1008.*input blocked by guardrail pii/);
    expect(events).toEqual(['error:input blocked by guardrail pii']);
    expect(call.closeCode).toBe(1008);
    expect(call.closeReason).toBe('policy');
  });

  it('uses the close reason when there was no error frame', async () => {
    const { sock, call } = await ready();
    sock.close(1008, 'blocked: prompt injection');
    const run = async () => {
      for await (const _ of call);
    };
    await expect(run()).rejects.toThrow(/1008.*blocked: prompt injection/);
  });

  it('rejects a consumer already parked on the next event', async () => {
    const { sock, call } = await ready();
    const pending = call[Symbol.asyncIterator]().next();
    sock.close(1008, 'policy');
    await expect(pending).rejects.toThrow(/1008.*policy/);
  });

  it('a normal close ends quietly and records the code', async () => {
    const { sock, call } = await ready();
    sock.frame({ type: 'turn_done' });
    sock.close(1000, 'bye');
    const types: string[] = [];
    for await (const ev of call) types.push(ev.type);
    expect(types).toEqual(['turn_done']);
    expect(call.closeCode).toBe(1000);
  });

  it('a close before ready reports the code and reason', async () => {
    const { sock, webSocket } = open();
    const p = connectRealtime({ url: 'http://h', agent: 'voice', webSocket });
    sock.emit('close', { code: 1008, reason: 'agent blocked' });
    await expect(p).rejects.toThrow(/closed before ready \(code 1008: agent blocked\)/);
  });
});

describe('no provider option', () => {
  it('the url never carries ?provider=', () => {
    expect(realtimeUrl('http://h', 'voice', 'marin', 'm')).not.toContain('provider');
    expect(realtimeUrl('http://h', 'voice', 'marin', 'm')).toBe('ws://h/api/v1/realtime/voice?voice=marin&model=m');
  });
});
