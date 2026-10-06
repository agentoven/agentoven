"""Realtime voice sessions with an AgentOven agent (Pro).

A session is one WebSocket carrying JSON text frames. Audio is PCM16, 24 kHz,
mono, base64 inside the frames — the same on every client.

    async with client.realtime("support-agent", voice="marin") as call:
        await call.send_audio(pcm_chunk)           # stream the microphone
        async for event in call:
            if event.type == "audio":
                speaker.write(event.audio)         # raw PCM16 bytes
            elif event.type == "interrupted":
                speaker.flush()                    # barge-in: drop queued speech
            elif event.type == "transcript" and event.final:
                print(event.role, event.text)

Requires the ``websockets`` package: ``pip install "agentoven[realtime]"``.

The agent always runs on its own provider, which must offer the ``realtime``
modality (see ``client.agent_card(name).supports("realtime")``).

Browsers cannot set headers on a WebSocket. A backend that holds the API key
mints a short-lived token with ``client.realtime_token(agent)`` and hands it to
the page, which opens the socket offering the WebSocket subprotocols
``agentoven.v1`` and ``agentoven-token.<token>``. ``client.realtime(agent,
token=...)`` does the same from Python.

If the agent's input guardrails block what the caller said, the server sends an
``error`` event and closes the socket with code 1008; iterating the session then
raises :class:`RealtimeError` carrying the reason.
"""

from __future__ import annotations

import base64
import json
import re
from dataclasses import dataclass
from typing import Any, AsyncIterator, Optional, Sequence
from urllib.parse import quote, urlencode

AUDIO_FORMAT = "pcm16-24khz-mono"

#: The WebSocket subprotocol the server selects.
WS_SUBPROTOCOL = "agentoven.v1"
#: Offered next to :data:`WS_SUBPROTOCOL`; the rest of the string is the token.
TOKEN_SUBPROTOCOL_PREFIX = "agentoven-token."
#: Close code the server uses when it ends a call for policy (input guardrails).
CLOSE_POLICY_VIOLATION = 1008

# RFC 6455: a subprotocol is an HTTP token — no spaces, separators or controls.
_SUBPROTOCOL_TOKEN = re.compile(r"^[!#$%&'*+\-.^_`|~0-9A-Za-z]+$")


class RealtimeError(Exception):
    """Raised when a session cannot be opened or breaks protocol."""


@dataclass
class RealtimeEvent:
    """One frame from the server.

    ``type`` is one of: ``ready``, ``audio``, ``transcript``, ``tool_call``,
    ``tool_result``, ``turn_done``, ``interrupted``, ``error``.

    ``interrupted`` means the caller spoke over the agent: stop playback and
    discard any audio you have queued. Only the fields relevant to the
    type are set.
    """

    type: str
    session_id: str = ""
    format: str = ""
    audio: bytes = b""
    role: str = ""
    text: str = ""
    final: bool = False
    tool_name: str = ""
    tool_args: str = ""
    tool_output: str = ""
    is_error: bool = False
    error: str = ""

    @classmethod
    def from_frame(cls, frame: dict[str, Any]) -> "RealtimeEvent":
        audio_b64 = frame.get("audio") or ""
        return cls(
            type=frame.get("type", ""),
            session_id=frame.get("session_id", ""),
            format=frame.get("format", ""),
            audio=base64.b64decode(audio_b64) if audio_b64 else b"",
            role=frame.get("role", ""),
            text=frame.get("text", ""),
            final=bool(frame.get("final", False)),
            tool_name=frame.get("tool_name", ""),
            tool_args=frame.get("tool_args", ""),
            tool_output=frame.get("tool_output", ""),
            is_error=bool(frame.get("is_error", False)),
            error=frame.get("error", ""),
        )


def check_subprotocol_token(token: str) -> None:
    """Refuse a token that cannot travel as a WebSocket subprotocol.

    Browsers throw on such a value; failing here gives a clearer message.
    """
    if not _SUBPROTOCOL_TOKEN.match(TOKEN_SUBPROTOCOL_PREFIX + token):
        raise ValueError("realtime token contains characters that are not valid in a WebSocket subprotocol")


def _close_info(exc: Any) -> tuple[Optional[int], str]:
    """The (code, reason) of a websockets ``ConnectionClosed``, when it has one."""
    frame = getattr(exc, "rcvd", None)
    return getattr(frame, "code", None), getattr(frame, "reason", "") or ""


def realtime_url(base_url: str, agent: str, voice: Optional[str] = None, model: Optional[str] = None) -> str:
    """The WebSocket URL for an agent's realtime endpoint."""
    base = base_url.rstrip("/")
    if base.startswith("https://"):
        base = "wss://" + base[len("https://"):]
    elif base.startswith("http://"):
        base = "ws://" + base[len("http://"):]
    query = {k: v for k, v in (("voice", voice), ("model", model)) if v}
    url = f"{base}/api/v1/realtime/{quote(agent, safe='')}"
    return f"{url}?{urlencode(query)}" if query else url


class RealtimeSession:
    """An open realtime call. Use as ``async with`` and iterate for events."""

    def __init__(self, ws: Any, ready: RealtimeEvent) -> None:
        self._ws = ws
        self.session_id = ready.session_id
        self.audio_format = ready.format
        #: Set once the server has closed the call.
        self.close_code: Optional[int] = None
        self.close_reason: str = ""
        self._last_error: str = ""

    async def _send(self, frame: dict[str, Any]) -> None:
        await self._ws.send(json.dumps(frame))

    async def send_audio(self, pcm: bytes) -> None:
        """Stream a chunk of the caller's speech (PCM16, 24 kHz, mono)."""
        await self._send({"type": "audio", "audio": base64.b64encode(pcm).decode("ascii")})

    async def commit(self) -> None:
        """End the caller's turn. Only needed when you do your own turn detection."""
        await self._send({"type": "commit"})

    async def send_text(self, text: str) -> None:
        """Send a typed turn instead of speech."""
        await self._send({"type": "text", "text": text})

    async def interrupt(self) -> None:
        """Stop the agent mid-response (barge-in)."""
        await self._send({"type": "interrupt"})

    async def close(self) -> None:
        await self._ws.close()

    def __aiter__(self) -> AsyncIterator[RealtimeEvent]:
        return self._events()

    async def _events(self) -> AsyncIterator[RealtimeEvent]:
        try:
            async for raw in self._ws:
                if isinstance(raw, bytes):
                    continue  # the protocol is text frames only
                event = RealtimeEvent.from_frame(json.loads(raw))
                if event.type == "error" and event.error:
                    self._last_error = event.error
                yield event
        except Exception as exc:  # a closed socket ends the stream, anything else is real
            if not type(exc).__name__.startswith("ConnectionClosed"):
                raise
            self.close_code, self.close_reason = _close_info(exc)
        else:
            self.close_code = getattr(self._ws, "close_code", None)
            self.close_reason = getattr(self._ws, "close_reason", "") or ""
        if self.close_code == CLOSE_POLICY_VIOLATION:
            why = self._last_error or self.close_reason or "blocked by the agent's guardrails"
            raise RealtimeError(f"realtime call ended by the server (close code 1008): {why}")

    async def __aenter__(self) -> "RealtimeSession":
        return self

    async def __aexit__(self, *exc: Any) -> None:
        await self.close()


class RealtimeConnection:
    """Awaitable async context manager that opens a :class:`RealtimeSession`."""

    def __init__(
        self,
        url: str,
        headers: dict[str, str],
        connect: Any = None,
        subprotocols: Optional[Sequence[str]] = None,
    ) -> None:
        self._url = url
        self._headers = headers
        self._connect = connect
        self._subprotocols = list(subprotocols) if subprotocols else None
        self._session: Optional[RealtimeSession] = None

    async def _open(self) -> RealtimeSession:
        connect = self._connect
        if connect is None:
            try:
                from websockets.asyncio.client import connect as ws_connect
            except ImportError as exc:  # pragma: no cover - exercised by message only
                raise RealtimeError(
                    'realtime voice needs the websockets package: pip install "agentoven[realtime]"'
                ) from exc
            connect = ws_connect
        try:
            options: dict[str, Any] = {"additional_headers": self._headers, "max_size": 16 * 2**20}
            if self._subprotocols:
                options["subprotocols"] = self._subprotocols
            ws = await connect(self._url, **options)
        except Exception as exc:
            response = getattr(exc, "response", None)
            status = getattr(response, "status_code", None)
            if status is not None:
                body = getattr(response, "body", b"") or b""
                detail = f": {body.decode(errors='replace').strip()}" if body.strip() else ""
                raise RealtimeError(f"realtime session refused (HTTP {status}){detail}") from exc
            raise RealtimeError(f"could not open realtime session: {exc}") from exc

        try:
            first = await ws.recv()
        except Exception as exc:
            if not type(exc).__name__.startswith("ConnectionClosed"):
                raise
            code, reason = _close_info(exc)
            suffix = f" (code {code}{': ' + reason if reason else ''})" if code else ""
            raise RealtimeError(f"realtime session closed before ready{suffix}") from exc
        frame = json.loads(first)
        ready = RealtimeEvent.from_frame(frame)
        if ready.type != "ready":
            await ws.close()
            raise RealtimeError(f"expected a ready frame, got {ready.type!r}: {ready.error}")
        return RealtimeSession(ws, ready)

    def __await__(self):  # type: ignore[no-untyped-def]
        return self._open().__await__()

    async def __aenter__(self) -> RealtimeSession:
        self._session = await self._open()
        return self._session

    async def __aexit__(self, *exc: Any) -> None:
        if self._session is not None:
            await self._session.close()
