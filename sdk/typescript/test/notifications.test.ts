import { describe, expect, it } from 'vitest';
import { channelUrl, listenToChannel, NotificationError } from '../src/notifications.js';
import type { WebSocketLike } from '../src/realtime.js';

/** A scriptable stand-in for a WebSocket. */
class FakeSocket implements WebSocketLike {
  closedWith: number | undefined;
  private fns: Record<string, Array<(e: any) => void>> = {};
  addEventListener(type: string, fn: (e: any) => void): void {
    (this.fns[type] ??= []).push(fn);
  }
  send(): void {}
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

describe('channelUrl', () => {
  it('maps the scheme and escapes the channel', () => {
    expect(channelUrl('http://localhost:8080', 'ops')).toBe('ws://localhost:8080/api/v1/channels/ops/ws');
    expect(channelUrl('https://ao.example.com/', 'my feed')).toBe('wss://ao.example.com/api/v1/channels/my%20feed/ws');
  });
});

describe('listenToChannel', () => {
  it('sends credentials, resolves on ready, and yields each notification until the socket closes', async () => {
    const { sock, webSocket, seen } = open();
    const p = listenToChannel({ url: 'http://x', channel: 'ops', apiKey: 'ao_key', kitchen: 'acme', webSocket });
    sock.frame({ type: 'ready', channel: 'ops', kitchen: 'acme' });
    const feed = await p;
    expect(seen().url).toBe('ws://x/api/v1/channels/ops/ws');
    expect(seen().headers).toEqual({ Authorization: 'Bearer ao_key', 'X-Kitchen': 'acme' });
    expect(seen().protocols).toBeUndefined();
    expect(feed.ready.channel).toBe('ops');

    sock.frame({ type: 'notification', payload: { message: 'm1' } });
    sock.frame({ type: 'notification', payload: { message: 'm2' } });
    setTimeout(() => sock.close(1000), 10);
    const got: string[] = [];
    for await (const n of feed) got.push(n.payload!.message as string);
    expect(got).toEqual(['m1', 'm2']);
  });

  it('a ticket connects the way a browser does, with no headers', async () => {
    const { sock, webSocket, seen } = open();
    const p = listenToChannel({ url: 'http://x', channel: 'ops', apiKey: 'ignored', token: 't.k', webSocket });
    sock.frame({ type: 'ready' });
    await p;
    expect(seen().protocols).toEqual(['agentoven.v1', 'agentoven-token.t.k']);
    expect(seen().headers).toEqual({});
  });

  it('refuses a ticket that cannot travel as a subprotocol', async () => {
    await expect(listenToChannel({ url: 'http://x', channel: 'ops', token: 'has space' })).rejects.toThrow(NotificationError);
  });

  it('rejects when the channel closes or errors before it is ready, and when the first frame is not ready', async () => {
    const a = open();
    const pa = listenToChannel({ url: 'http://x', channel: 'ops', webSocket: a.webSocket });
    a.sock.close(1008, 'no');
    await expect(pa).rejects.toThrow(/before it was ready \(code 1008: no\)/);
    const b = open();
    const pb = listenToChannel({ url: 'http://x', channel: 'ops', webSocket: b.webSocket });
    b.sock.emit('error');
    await expect(pb).rejects.toThrow(/could not connect/);
    const c = open();
    const pc = listenToChannel({ url: 'http://x', channel: 'ops', webSocket: c.webSocket });
    c.sock.frame({ type: 'notification' });
    await expect(pc).rejects.toThrow(/expected a ready frame/);
  });

  it('says why a listener was cut off', async () => {
    const { sock, webSocket } = open();
    const p = listenToChannel({ url: 'http://x', channel: 'ops', webSocket });
    sock.frame({ type: 'ready' });
    const feed = await p;
    setTimeout(() => sock.close(1013, 'listener fell behind'), 10);
    await expect((async () => { for await (const _ of feed) { /* wait */ } })()).rejects.toThrow(/1013: listener fell behind/);
  });
});
