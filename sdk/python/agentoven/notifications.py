"""
agentoven.notifications — listen to a live-feed notification channel.

A channel of kind ``websocket`` pushes every notification sent through it, as one JSON message each, to the
connections listening at ``/api/v1/channels/{name}/ws``. A recipe's ``notify`` step sends through it, and so
do the platform's own events (an approval waiting, a step failing) for channels that subscribe to them.

    client.create_notification_channel("ops")          # once

    async with client.listen("ops") as feed:
        async for message in feed:
            print(message["payload"]["message"])

Nothing is kept: a listener sees what is sent after it connects. The first frame, ``{"type": "ready"}``, is
read for you. Requires the ``websockets`` package: ``pip install "agentoven[realtime]"``.

A browser cannot send an API key on a WebSocket. A backend that holds the key asks for a ticket
(``client.notification_ticket("ops")``), good for a minute and for that one channel, and the page connects
with ``Sec-WebSocket-Protocol: agentoven.v1, agentoven-token.<ticket>``.
"""

from __future__ import annotations

import json
from typing import Any, AsyncIterator, Dict, Optional, Sequence
from urllib.parse import quote

__all__ = ["NotificationError", "NotificationListener", "channel_url"]

WS_SUBPROTOCOL = "agentoven.v1"
TICKET_SUBPROTOCOL_PREFIX = "agentoven-token."


class NotificationError(Exception):
    """A listener could not connect, or the server ended it."""


def channel_url(base_url: str, channel: str) -> str:
    """The WebSocket address for a channel."""
    base = base_url.rstrip("/")
    if base.startswith("https://"):
        base = "wss://" + base[len("https://"):]
    elif base.startswith("http://"):
        base = "ws://" + base[len("http://"):]
    return f"{base}/api/v1/channels/{quote(channel, safe='')}/ws"


class NotificationListener:
    """An open connection to a channel. Iterate it for notifications; it ends when the connection does."""

    def __init__(self, ws: Any, ready: Dict[str, Any]) -> None:
        self._ws = ws
        #: The first frame the server sent: ``{"type": "ready", "channel": ..., "kitchen": ...}``.
        self.ready = ready

    async def close(self) -> None:
        await self._ws.close()

    async def __aiter__(self) -> AsyncIterator[Dict[str, Any]]:
        try:
            async for raw in self._ws:
                yield json.loads(raw)
        except Exception as exc:
            if not type(exc).__name__.startswith("ConnectionClosed"):
                raise
            code = getattr(getattr(exc, "rcvd", None), "code", None)
            if code not in (None, 1000, 1001):  # a normal close just ends the iteration
                reason = getattr(getattr(exc, "rcvd", None), "reason", "") or ""
                raise NotificationError(f"the listener was closed (code {code}{': ' + reason if reason else ''})") from exc


class NotificationConnection:
    """Awaitable async context manager that opens a :class:`NotificationListener`."""

    def __init__(self, url: str, headers: Dict[str, str], subprotocols: Optional[Sequence[str]] = None, connect: Any = None) -> None:
        self._url = url
        self._headers = headers
        self._subprotocols = list(subprotocols or [WS_SUBPROTOCOL])
        self._connect = connect
        self._listener: Optional[NotificationListener] = None

    async def _open(self) -> NotificationListener:
        connect = self._connect
        if connect is None:
            try:
                from websockets.asyncio.client import connect as ws_connect
            except ImportError as exc:  # pragma: no cover - message only
                raise NotificationError('listening needs the websockets package: pip install "agentoven[realtime]"') from exc
            connect = ws_connect
        try:
            ws = await connect(self._url, additional_headers=self._headers, subprotocols=self._subprotocols)
        except Exception as exc:
            response = getattr(exc, "response", None)
            status = getattr(response, "status_code", None)
            if status is not None:
                body = getattr(response, "body", b"") or b""
                detail = f": {body.decode(errors='replace').strip()}" if body.strip() else ""
                raise NotificationError(f"the channel refused the connection (HTTP {status}){detail}") from exc
            raise NotificationError(f"could not connect to the channel: {exc}") from exc
        first = json.loads(await ws.recv())
        if first.get("type") != "ready":
            await ws.close()
            raise NotificationError(f"expected a ready frame, got {first.get('type')!r}")
        return NotificationListener(ws, first)

    def __await__(self):  # type: ignore[no-untyped-def]
        return self._open().__await__()

    async def __aenter__(self) -> NotificationListener:
        self._listener = await self._open()
        return self._listener

    async def __aexit__(self, *exc: Any) -> None:
        if self._listener is not None:
            await self._listener.close()
