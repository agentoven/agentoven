# @agentoven/sdk

TypeScript SDK for [AgentOven](https://agentoven.dev) — the enterprise agent control plane.

```ts
import { ProClient } from '@agentoven/sdk';

const pro = new ProClient({ url: 'http://localhost:8080', apiKey: process.env.AGENTOVEN_API_KEY, kitchen: 'default' });
```

## Modalities

A *modality* is something an agent can take in or give out beyond text: `image`,
`pdf`, `video`, `audio` (cascaded voice: speech-to-text in, text-to-speech out)
and `realtime` (live speech-to-speech). Modalities belong to the **provider**: an
agent has exactly the modalities of its own provider. Text is always on and has
no entry. You do not have to set anything: defaults come from the provider
driver and the model catalog.

### Enable or disable modalities on a provider

```ts
import { ProClient, modalities } from '@agentoven/sdk';

await pro.createProvider({
  name: 'openai',
  kind: 'openai',
  api_key: process.env.OPENAI_API_KEY,
  models: ['gpt-4o'],
  modalities: modalities()
    .pdf(false) // switch PDF input off
    .video(true) // widen for a gateway the catalog doesn't know
    .audio({ sttModel: 'whisper-1', ttsModel: 'tts-1' })
    .realtime({ enabled: true, model: 'gpt-realtime' }),
});

// Updates are merged per modality. null / clear() deletes a key (or a whole
// modality) and puts it back on its default.
await pro.updateProvider('openai', { modalities: modalities().clear('pdf').audio({ ttsModel: 'tts-1-hd' }) });

const provider = await pro.getProvider('openai');
console.log(provider.modalities); // effective: ['text', 'image', 'audio', ...]
```

The rules the server enforces are checked before the request is sent (unknown
modality, an entry for `text`, unknown settings, wrong types) and throw
`ModalitiesError`. `audio` and `realtime` cannot be enabled beyond what the
provider kind supports; the server answers 400 (`AgentOvenAPIError`).

### Check what an agent can do

```ts
import { supportsModality } from '@agentoven/sdk';

const card = await pro.agentCard('support-agent');
console.log(card.modalities); // e.g. ['text', 'image', 'audio', 'realtime']
if (supportsModality(card, 'audio')) {
  /* ... */
}
await pro.supports('support-agent', 'pdf'); // shorthand
```

### Send an image or PDF

```ts
import { Attachment } from '@agentoven/sdk';

const session = await pro.createAgentSession('vision-agent');
const reply = await pro.sendSessionMessage('vision-agent', session.id, {
  content: 'Summarise these.',
  attachments: [
    await Attachment.fromPath('chart.png'), // Node; MIME type inferred
    await Attachment.fromPath('report.pdf'),
    Attachment.fromUrl('https://example.com/photo.jpg'),
    Attachment.fromBytes(new Uint8Array(await file.arrayBuffer()), { mimeType: 'image/webp' }), // browser
  ],
});
console.log(reply.content);
```

Audio and video attachments work the same way. Audio is transcribed by the
server first, so any agent whose provider offers `audio` can take it.

### Cascaded voice

```ts
import { audioFromPath, saveAudio, AudioModalityError } from '@agentoven/sdk';

try {
  const r = await pro.invoke('support-agent', {
    audio: await audioFromPath('question.wav'), // or audioFromBytes(...), audioFromBase64(...)
    voiceOutput: true,
    voice: 'alloy',
  });
  console.log(r.transcript); // what the agent heard
  console.log(r.response); // its text reply
  await saveAudio(r.audio!, 'answer'); // decoded bytes in r.audio.data -> answer.mp3
} catch (err) {
  if (err instanceof AudioModalityError) console.log("this agent's provider does not offer the audio modality");
  else throw err;
}
```

### Realtime (live speech-to-speech)

The agent always runs on its own provider, which must offer the `realtime`
modality. Server-side (Node), with your API key:

```ts
const call = await pro.realtime('support-agent', { voice: 'marin' });
call.sendAudio(pcmChunk); // PCM16, 24 kHz, mono
for await (const ev of call) {
  if (ev.type === 'audio') speaker.write(ev.audio!);
  if (ev.type === 'transcript' && ev.final) console.log(ev.role, ev.text);
}
```

If the agent's input guardrails block what the caller said you get an `error`
event, then the loop rejects with a `RealtimeError` (close code 1008) carrying
the reason; `call.closeCode` / `call.closeReason` are set once the call ends.

### Realtime in a browser

Browsers cannot set headers on a WebSocket. Your backend, which holds the API
key, mints a short-lived token (bound to the agent and kitchen, checked only when
the socket opens):

```ts
// backend
const { token, expires_in } = await pro.realtimeToken('support-agent', 60); // 1-300 seconds
res.json({ token });
```

The page connects with no auth headers; the SDK offers the WebSocket
subprotocols `['agentoven.v1', 'agentoven-token.<token>']`:

```ts
// browser
import { connectRealtime } from '@agentoven/sdk';

const { token } = await (await fetch('/my-backend/realtime-token')).json();
const call = await connectRealtime({ url: 'https://agentoven.example.com', agent: 'support-agent', token });
```
