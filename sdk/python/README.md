# agentoven

Python SDK for [AgentOven](https://agentoven.dev) — the enterprise agent control plane.

Built with native Rust performance via [PyO3](https://pyo3.rs).

## Install

```bash
pip install agentoven
```

## Quick Start

```python
from agentoven import AgentOvenClient, Agent

# Connect to the control plane
client = AgentOvenClient(url="http://localhost:8080")

# Register an agent
agent = Agent(name="my-assistant", description="A helpful AI assistant")
result = client.register_agent(agent)
print(result)

# List all agents
agents = client.list_agents()
for a in agents:
    print(a)

# Deploy an agent
client.bake("my-assistant")

# Pause an agent
client.cool("my-assistant")
```

## Features

- **Native performance** — Rust core compiled to a Python extension
- **A2A protocol** — built on the Agent-to-Agent open standard
- **Multi-framework** — works with LangGraph, CrewAI, AutoGen, and more
- **Kitchen isolation** — workspace-based multi-tenancy

## License

MIT


## Realtime voice (Pro)

```python
# pip install "agentoven[realtime]"
async with client.realtime("support-agent", voice="marin") as call:
    await call.send_audio(pcm_chunk)      # PCM16, 24 kHz, mono
    async for event in call:
        if event.type == "audio":
            speaker.write(event.audio)    # raw PCM16 bytes
        elif event.type == "transcript" and event.final:
            print(event.role, event.text)
```

The agent always runs on its own provider, which must offer the `realtime`
modality. If the agent's input guardrails block what the caller said, you get an
`error` event and then iterating raises `RealtimeError` (close code 1008) with the
reason; `call.close_code` / `call.close_reason` are set once the call ends.

Browsers cannot set headers on a WebSocket: see "Realtime in a browser" below.


## Modalities

A *modality* is something an agent can take in or give out beyond text: `image`,
`pdf`, `video`, `audio` (cascaded voice: speech-to-text in, text-to-speech out)
and `realtime` (live speech-to-speech). Modalities belong to the **provider**: an
agent has exactly the modalities of its own provider. Text is always on and has
no entry. You do not have to set anything: defaults come from the provider
driver and the model catalog.

### Enable or disable modalities on a provider

```python
from agentoven import AgentOvenClient, Modalities, AudioConfig, RealtimeConfig, CLEAR

client = AgentOvenClient(url="http://localhost:8080", api_key="...")

client.create_provider(
    "openai", "openai", api_key="sk-...", models=["gpt-4o"],
    modalities=Modalities(
        pdf=False,                                    # switch PDF input off
        video=True,                                   # widen for a gateway the catalog doesn't know
        audio=AudioConfig(stt_model="whisper-1", tts_model="tts-1"),
        realtime=RealtimeConfig(enabled=True, model="gpt-realtime"),
    ),
)

# Updates are merged per modality. CLEAR deletes a key (or a whole modality)
# and puts it back on its default.
client.update_provider("openai", modalities=Modalities(pdf=CLEAR, audio=AudioConfig(tts_model="tts-1-hd")))

provider = client.get_provider("openai")
print(provider.modalities)          # effective: ['text', 'image', 'audio', ...]
print(provider.supports("audio"))
```

The same rules the server enforces are checked before the request is sent
(unknown modality, an entry for `text`, unknown settings, wrong types) and raise
`ModalitiesError`. `audio` and `realtime` cannot be enabled beyond what the
provider kind supports; the server answers 400 (`AgentOvenAPIError`).

### Check what an agent can do

```python
card = client.agent_card("support-agent")
print(card.modalities)              # e.g. ['text', 'image', 'audio', 'realtime']
if card.supports("audio"):
    ...
client.supports("support-agent", "pdf")   # shorthand
```

### Send an image or PDF

```python
from agentoven import Attachment

session = client.create_agent_session("vision-agent")
reply = client.send_session_message(
    "vision-agent", session["id"], "Summarise these.",
    attachments=[
        Attachment.from_path("chart.png"),                       # MIME type inferred
        Attachment.from_path("report.pdf"),
        Attachment.from_url("https://example.com/photo.jpg"),
        Attachment.from_bytes(raw_bytes, mime_type="image/webp"),
    ],
)
print(reply["content"])
```

Audio and video attachments work the same way. Audio is transcribed by the
server first, so any agent whose provider offers `audio` can take it.

### Cascaded voice

```python
from agentoven import AudioModalityError

try:
    result = client.invoke(
        "support-agent",
        audio="question.wav",        # path, Path, bytes, or AudioInput.from_base64(...)
        voice_output=True,
        voice="alloy",
    )
except AudioModalityError:
    print("this agent's provider does not offer the audio modality")
else:
    print(result.transcript)         # what the agent heard
    print(result.response)           # its text reply
    result.save_audio("answer")      # decoded bytes in result.audio.data -> answer.mp3
```

### Realtime (live speech-to-speech)

Server-side, with your API key (needs `pip install "agentoven[realtime]"`):

```python
async with client.realtime("support-agent", voice="marin") as call:
    await call.send_audio(pcm_chunk)
    async for event in call:
        ...
```

### Realtime in a browser

Browsers cannot set headers on a WebSocket. Your backend mints a short-lived
token (bound to the agent and kitchen, checked only when the socket opens) and
gives it to the page:

```python
minted = client.realtime_token("support-agent", ttl_seconds=60)   # 1-300
return {"token": minted.token, "expires_in": minted.expires_in}   # your endpoint
```

The page opens the socket with the WebSocket subprotocols
`["agentoven.v1", "agentoven-token.<token>"]` (the TypeScript SDK does this with
`connectRealtime({ token })`). From Python the same flow, with no auth headers:

```python
async with AgentOvenClient(url="http://localhost:8080").realtime("support-agent", token=minted.token) as call:
    ...
```
