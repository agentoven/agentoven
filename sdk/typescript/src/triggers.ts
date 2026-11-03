/**
 * The headers that sign a delivery to a webhook trigger (Pro ADR-0047): `X-AgentOven-Signature`
 * (`sha256=` and the HMAC-SHA256 of `"<timestamp>.<body>"`), `X-AgentOven-Timestamp`, and an
 * `X-AgentOven-Delivery` id that is the same on every retry of one delivery. This makes a fresh id; pass
 * `deliveryId` to keep one on a retry. It uses WebCrypto, so it runs in Node 18+ and in a browser.
 */
export async function signWebhook(secret: string, body: string | Uint8Array, opts: { timestamp?: number; deliveryId?: string } = {}): Promise<Record<string, string>> {
  const ts = Math.floor(opts.timestamp ?? Date.now() / 1000);
  const enc = new TextEncoder();
  const payload = typeof body === 'string' ? enc.encode(body) : body;
  const prefix = enc.encode(`${ts}.`);
  const message = new Uint8Array(prefix.length + payload.length);
  message.set(prefix);
  message.set(payload, prefix.length);
  const key = await crypto.subtle.importKey('raw', enc.encode(secret), { name: 'HMAC', hash: 'SHA-256' }, false, ['sign']);
  const mac = new Uint8Array(await crypto.subtle.sign('HMAC', key, message));
  const hex = Array.from(mac, (b) => b.toString(16).padStart(2, '0')).join('');
  return {
    'X-AgentOven-Signature': `sha256=${hex}`,
    'X-AgentOven-Timestamp': String(ts),
    'X-AgentOven-Delivery': opts.deliveryId ?? crypto.randomUUID(),
  };
}
