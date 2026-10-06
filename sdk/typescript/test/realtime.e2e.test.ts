import { describe, expect, it } from 'vitest';
import { connectRealtime } from '../src/realtime.js';

// Opt-in: needs a real WebSocket server that replies per the realtime protocol
// and echoes the Authorization header it received in the ready frame's
// session_id. Set AGENTOVEN_REALTIME_E2E_URL=http://127.0.0.1:<port> to run.
const base = process.env.AGENTOVEN_REALTIME_E2E_URL;

describe.skipIf(!base)('default WebSocket factory against a real server', () => {
  it('delivers auth headers and streams events', async () => {
    const call = await connectRealtime({ url: base!, agent: 'voice', apiKey: 'ao_key', kitchen: 'payments' });
    expect(call.sessionId).toBe('Bearer ao_key|payments');
    call.sendText('ping');
    const events = [];
    for await (const ev of call) events.push(ev);
    expect(events.map((e) => e.type)).toEqual(['transcript', 'audio']);
    expect(events[0]).toMatchObject({ role: 'assistant', text: 'pong' });
    expect(Array.from(events[1].audio!)).toEqual([7, 8, 9]);
  });
});
