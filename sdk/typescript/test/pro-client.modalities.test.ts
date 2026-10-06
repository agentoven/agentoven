import { afterAll, beforeAll, beforeEach, describe, expect, it } from 'vitest';
import { Attachment, audioFromBytes } from '../src/media.js';
import { ModalitiesError, modalities } from '../src/modalities.js';
import { AgentOvenAPIError, AudioModalityError, ProClient } from '../src/pro-client.js';
import { FakeControlPlane } from './fake-server.js';

const api = new FakeControlPlane();
let pro: ProClient;

beforeAll(async () => {
  await api.start();
});
afterAll(async () => {
  await api.stop();
});
beforeEach(() => {
  api.requests = [];
  pro = new ProClient({ url: api.url, apiKey: 'ao_key', kitchen: 'payments' });
});

const ascii = (s: string) => Array.from(s).map((c) => c.charCodeAt(0));
const WAV = new Uint8Array([...ascii('RIFF'), 36, 0, 0, 0, ...ascii('WAVEfmt '), 0, 0, 0, 0]);
const PNG = new Uint8Array([0x89, ...ascii('PNG\r\n'), 0x1a, 0x0a, 0, 0, 0, 0]);
const b64 = (u: Uint8Array) => btoa(String.fromCharCode(...u));

const PROVIDER = {
  id: 'p1',
  name: 'openai',
  kind: 'openai',
  models: ['gpt-4o'],
  config: { api_key: '***', modalities: { pdf: { enabled: false } } },
  is_default: true,
  modalities: ['text', 'image', 'audio'],
};

describe('providers', () => {
  it('createProvider sends modalities under config', async () => {
    api.reply('POST', '/api/v1/models/providers', PROVIDER, 201);
    await pro.createProvider({
      name: 'openai',
      kind: 'openai',
      api_key: 'sk-test',
      models: ['gpt-4o'],
      endpoint: 'https://example.test/v1',
      modalities: modalities().pdf(false).audio({ sttModel: 'whisper-1', ttsModel: 'tts-1' }),
    });
    expect(api.last.headers.authorization).toBe('Bearer ao_key');
    expect(api.last.headers['x-kitchen']).toBe('payments');
    expect(api.last.body).toEqual({
      name: 'openai',
      kind: 'openai',
      models: ['gpt-4o'],
      endpoint: 'https://example.test/v1',
      is_default: false,
      config: {
        api_key: 'sk-test',
        modalities: { pdf: { enabled: false }, audio: { stt_model: 'whisper-1', tts_model: 'tts-1' } },
      },
    });
  });

  it('accepts a wire-format object and validates before sending', async () => {
    api.reply('POST', '/api/v1/models/providers', PROVIDER, 201);
    await pro.createProvider({ name: 'p', kind: 'openai', modalities: { video: { enabled: true } } });
    expect((api.last.body as any).config).toEqual({ modalities: { video: { enabled: true } } });

    const before = api.requests.length;
    await expect(pro.createProvider({ name: 'p', kind: 'openai', modalities: { vidio: {} } as never })).rejects.toThrow(ModalitiesError);
    await expect(pro.createProvider({ name: 'p', kind: 'openai', modalities: modalities().clear('pdf') })).rejects.toThrow(/pdf must be an object/);
    await expect(
      pro.createProvider({ name: 'p', kind: 'openai', config: { modalities: {} }, modalities: modalities() }),
    ).rejects.toThrow(/not both/);
    await expect(pro.createProvider({ name: 'p', kind: 'openai', config: { modalities: { text: {} } } })).rejects.toThrow(/text is always on/);
    expect(api.requests.length).toBe(before);
  });

  it('a server rejection is an AgentOvenAPIError', async () => {
    api.reply('POST', '/api/v1/models/providers', { error: 'audio is not supported by kind anthropic' }, 400);
    const err = await pro.createProvider({ name: 'a', kind: 'anthropic', modalities: modalities().audio(true) }).catch((e) => e);
    expect(err).toBeInstanceOf(AgentOvenAPIError);
    expect(err).not.toBeInstanceOf(AudioModalityError);
    expect(err.statusCode).toBe(400);
  });

  it('updateProvider merges and preserves is_default', async () => {
    api.reply('GET', '/api/v1/models/providers/openai', PROVIDER);
    api.reply('PUT', '/api/v1/models/providers/openai', { provider: PROVIDER, agents_burnt: 2 });
    const res = await pro.updateProvider('openai', { modalities: modalities().clear('pdf').audio({ ttsModel: 'tts-1-hd' }) });
    expect(api.requests.map((r) => r.method)).toEqual(['GET', 'PUT']);
    expect(api.last.body).toEqual({
      is_default: true,
      config: { modalities: { pdf: null, audio: { tts_model: 'tts-1-hd' } } },
    });
    expect(res.agents_burnt).toBe(2);
    expect(res.provider.name).toBe('openai');
  });

  it('updateProvider skips the read when is_default is given, and validates first', async () => {
    api.reply('PUT', '/api/v1/models/providers/openai', { provider: PROVIDER, agents_burnt: 0 });
    await pro.updateProvider('openai', { is_default: false, models: ['m'], api_key: 'sk-new', kind: 'openai' });
    expect(api.requests.map((r) => r.method)).toEqual(['PUT']);
    expect(api.last.body).toEqual({ kind: 'openai', models: ['m'], config: { api_key: 'sk-new' }, is_default: false });

    api.requests = [];
    await expect(pro.updateProvider('openai', { is_default: false, modalities: { image: { enabled: 'off' } } as never })).rejects.toThrow(/enabled must be true or false/);
    expect(api.requests).toEqual([]);
  });

  it('get and list expose effective modalities', async () => {
    api.reply('GET', '/api/v1/models/providers/open%20ai', PROVIDER);
    api.reply('GET', '/api/v1/models/providers', [PROVIDER]);
    expect((await pro.getProvider('open ai')).modalities).toEqual(['text', 'image', 'audio']);
    expect((await pro.listProviders())[0].name).toBe('openai');
  });
});

describe('agent card', () => {
  const CARD = {
    name: 'support',
    url: '/agents/support/a2a',
    capabilities: { streaming: true, vision: true },
    defaultInputModes: ['text', 'image', 'audio'],
    defaultOutputModes: ['text', 'audio'],
    modalities: ['text', 'image', 'audio', 'realtime'],
  };

  it('exposes modalities and answers supports()', async () => {
    api.reply('GET', '/api/v1/agents/support/card', CARD);
    const card = await pro.agentCard('support');
    expect(card.modalities).toEqual(['text', 'image', 'audio', 'realtime']);
    expect(card.defaultInputModes).toContain('audio');
    expect(await pro.supports('support', 'audio')).toBe(true);
    expect(await pro.supports('support', 'realtime')).toBe(true);
    expect(await pro.supports('support', 'video')).toBe(false);
    expect(await pro.supports('support', 'text')).toBe(true);
  });
});

describe('invoke (cascaded voice)', () => {
  const MP3 = new Uint8Array([...ascii('ID3'), 4, 0, 9, 9]);
  const OK = {
    agent: 'support',
    response: 'Your order shipped.',
    trace_id: 't-1',
    session_id: 's-9',
    turns: 1,
    usage: { total_tokens: 12 },
    latency_ms: 321,
    transcript: 'where is my order',
    audio: { data: b64(MP3), mime_type: 'audio/mpeg' },
  };

  it('sends audio and voice options, and decodes transcript and audio', async () => {
    api.reply('POST', '/api/v1/agents/support/invoke', OK);
    const r = await pro.invoke('support', { audio: audioFromBytes(WAV), voiceOutput: true, voice: 'alloy' });
    expect(api.last.body).toEqual({
      audio: { data: b64(WAV), mime_type: 'audio/wav' },
      voice_output: true,
      voice: 'alloy',
    });
    expect(r.transcript).toBe('where is my order');
    expect(r.response).toBe('Your order shipped.');
    expect(r.sessionId).toBe('s-9');
    expect(r.traceId).toBe('t-1');
    expect(r.latencyMs).toBe(321);
    expect(r.audio!.mimeType).toBe('audio/mpeg');
    expect(r.audio!.extension).toBe('.mp3');
    expect(Array.from(r.audio!.data)).toEqual(Array.from(MP3));
  });

  it('accepts raw bytes (sniffed, or with audioMimeType) and combines with a message', async () => {
    api.reply('POST', '/api/v1/agents/support/invoke', OK);
    await pro.invoke('support', { message: 'also this', audio: WAV });
    expect((api.last.body as any).message).toBe('also this');
    expect((api.last.body as any).audio.mime_type).toBe('audio/wav');
    expect((api.last.body as any).voice_output).toBeUndefined();
    await pro.invoke('support', { audio: new Uint8Array([1, 2, 3]), audioMimeType: 'audio/flac' });
    expect((api.last.body as any).audio.mime_type).toBe('audio/flac');
  });

  it('text only has no transcript or audio', async () => {
    api.reply('POST', '/api/v1/agents/support/invoke', { agent: 'support', response: 'hi', turns: 1 });
    const r = await pro.invoke('support', { message: 'hello', variables: { a: 'b' }, sessionId: 's' });
    expect(api.last.body).toEqual({ message: 'hello', variables: { a: 'b' }, session_id: 's' });
    expect(r.transcript).toBeUndefined();
    expect(r.audio).toBeUndefined();
  });

  it('checks arguments before sending', async () => {
    const before = api.requests.length;
    await expect(pro.invoke('support', {})).rejects.toThrow(/message, audio, or both/);
    await expect(pro.invoke('support', { message: 'hi', voice: 'alloy' })).rejects.toThrow(/voiceOutput/);
    await expect(pro.invoke('support', { audio: new Uint8Array([1, 2, 3]) })).rejects.toThrow(/pass mimeType/);
    expect(api.requests.length).toBe(before);
  });

  it('a provider without audio rejects with AudioModalityError', async () => {
    const msg =
      'agent "support" cannot take or give speech: its provider "anthropic" (anthropic) does not offer the audio modality (it offers: text, image, pdf)';
    api.reply('POST', '/api/v1/agents/support/invoke', { error: msg }, 400);
    const err = await pro.invoke('support', { audio: audioFromBytes(WAV) }).catch((e) => e);
    expect(err).toBeInstanceOf(AudioModalityError);
    expect(err).toBeInstanceOf(AgentOvenAPIError);
    expect(err.statusCode).toBe(400);
    expect(err.reason).toContain('does not offer the audio modality');
  });

  it('other 400s stay plain API errors', async () => {
    api.reply('POST', '/api/v1/agents/support/invoke', { error: 'could not transcribe audio: bad' }, 400);
    const err = await pro.invoke('support', { audio: audioFromBytes(WAV) }).catch((e) => e);
    expect(err).toBeInstanceOf(AgentOvenAPIError);
    expect(err).not.toBeInstanceOf(AudioModalityError);
  });
});

describe('agent sessions with attachments', () => {
  it('creates a session and sends content_parts', async () => {
    api.reply('POST', '/api/v1/agents/vision/sessions', { id: 's-1', status: 'active' }, 201);
    api.reply('POST', '/api/v1/agents/vision/sessions/s-1/messages', {
      session_id: 's-1',
      turn_number: 1,
      content: 'A cat.',
      status: 'active',
    });
    const session = await pro.createAgentSession('vision', { maxTurns: 5, metadata: { u: 1 } });
    expect(session.id).toBe('s-1');
    expect(api.last.body).toEqual({ max_turns: 5, metadata: { u: 1 } });

    const reply = await pro.sendSessionMessage('vision', 's-1', {
      content: 'What is in these?',
      attachments: [
        Attachment.fromBytes(PNG, { name: 'cat.png' }),
        Attachment.fromUrl('https://cdn.test/talk.mp4'),
        Attachment.fromBytes(WAV),
        Attachment.fromBytes(new Uint8Array(ascii('%PDF-1.7')), { name: 'invoice.pdf' }),
      ],
      promptVars: { lang: 'en' },
    });
    expect(reply.content).toBe('A cat.');
    const body = api.last.body as any;
    expect(body.content).toBe('What is in these?');
    expect(body.prompt_vars).toEqual({ lang: 'en' });
    expect(body.content_parts.map((p: any) => p.type)).toEqual(['image', 'video', 'audio', 'file']);
    expect(body.content_parts[0].media).toEqual({ mime_type: 'image/png', data: b64(PNG), name: 'cat.png' });
    expect(body.content_parts[1].media).toEqual({ mime_type: 'video/mp4', url: 'https://cdn.test/talk.mp4' });
  });

  it('allows attachments only, but not an empty message', async () => {
    api.reply('POST', '/api/v1/agents/a/sessions/s/messages', { content: 'ok' });
    await pro.sendSessionMessage('a', 's', { attachments: [Attachment.fromBytes(PNG)] });
    expect((api.last.body as any).content).toBe('');
    await expect(pro.sendSessionMessage('a', 's', {})).rejects.toThrow(/needs content/);
  });

  it('audio on a provider without audio rejects with AudioModalityError', async () => {
    api.reply('POST', '/api/v1/agents/a/sessions/s/messages', { error: 'its provider "x" (anthropic) does not offer the audio modality' }, 400);
    await expect(pro.sendSessionMessage('a', 's', { attachments: [Attachment.fromBytes(WAV)] })).rejects.toBeInstanceOf(AudioModalityError);
  });
});

describe('realtimeToken', () => {
  it('posts the ttl with normal auth', async () => {
    api.reply('POST', '/api/v1/realtime/my%20voice/token', { token: 'opaque.tok', expires_in: 60 });
    const t = await pro.realtimeToken('my voice');
    expect(api.last.body).toEqual({ ttl_seconds: 60 });
    expect(api.last.headers.authorization).toBe('Bearer ao_key');
    expect(api.last.headers['x-kitchen']).toBe('payments');
    expect(t).toEqual({ token: 'opaque.tok', expires_in: 60 });

    api.reply('POST', '/api/v1/realtime/voice/token', { token: 't2', expires_in: 300 });
    expect((await pro.realtimeToken('voice', 300)).expires_in).toBe(300);
    expect(api.last.body).toEqual({ ttl_seconds: 300 });
  });

  it('checks the ttl client-side', async () => {
    const before = api.requests.length;
    for (const ttl of [0, -5, 301, 1.5, Number.NaN]) {
      await expect(pro.realtimeToken('voice', ttl)).rejects.toThrow(/between 1 and 300/);
    }
    expect(api.requests.length).toBe(before);
  });

  it('a failure is an API error', async () => {
    api.reply('POST', '/api/v1/realtime/ghost/token', { error: 'agent not found' }, 404);
    const err = await pro.realtimeToken('ghost').catch((e) => e);
    expect(err).toBeInstanceOf(AgentOvenAPIError);
    expect(err.statusCode).toBe(404);
  });
});
