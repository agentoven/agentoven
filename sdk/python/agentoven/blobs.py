"""Upload a file once, then name it in messages by ``blob_ref``.

    from agentoven.blobs import Blobs

    blobs = Blobs(client)                      # client.blobs, once the client exposes it
    blob = blobs.upload("contract.pdf")        # path, bytes or Attachment (with data)
    client.send_session_message("legal", sid, "summarise", attachments=[blob])
    client.invoke("legal", "summarise", attachments=[blob])

A blob belongs to the kitchen that uploaded it: another kitchen gets a 404 for the
ref, so a ref is not a credential. The server's size limit is
``AGENTOVEN_BLOB_MAX_BYTES`` (25 MB by default) and unused blobs expire after
``AGENTOVEN_BLOB_TTL_DAYS`` (7 by default); an upload over the limit raises
:class:`AgentOvenAPIError` with status 413.
"""

from __future__ import annotations

import json
import os
import urllib.error
import urllib.parse
import urllib.request
from dataclasses import dataclass
from typing import TYPE_CHECKING, Any, Optional, Union

from agentoven.media import Attachment, _part_type, _read, guess_mime_type, sniff_mime_type

if TYPE_CHECKING:  # pragma: no cover
    from agentoven.client import AgentOvenClient


@dataclass(frozen=True)
class Blob:
    """A file stored on the control plane. Pass it wherever an attachment goes."""

    ref: str
    mime_type: str
    name: Optional[str] = None
    size: int = 0

    @property
    def type(self) -> str:
        """The content part type: ``image``, ``audio``, ``video`` or ``file`` (PDF)."""
        return _part_type(self.mime_type)

    def to_content_part(self) -> dict[str, Any]:
        """The ``content_parts`` entry that refers to the stored file."""
        media: dict[str, Any] = {"mime_type": self.mime_type, "blob_ref": self.ref}
        if self.name:
            media["name"] = self.name
        return {"type": self.type, "media": media}


class Blobs:
    """``POST/GET/DELETE /api/v1/blobs`` for one client."""

    def __init__(self, client: "AgentOvenClient") -> None:
        self._client = client

    def upload(
        self,
        source: Union[str, "os.PathLike[str]", bytes, Attachment],
        mime_type: Optional[str] = None,
        name: Optional[str] = None,
    ) -> Blob:
        """Store a file and return its :class:`Blob`.

        ``source`` is a path, raw bytes, or an :class:`Attachment` built from
        bytes or a path (one built from a URL has nothing to upload). For bytes
        the type is sniffed from the content unless ``mime_type`` is given.
        """
        if isinstance(source, Attachment):
            if source.data is None:
                raise ValueError("an attachment built from a URL has no bytes to upload")
            data, mime, name = source.data, mime_type or source.mime_type, name or source.name
        elif isinstance(source, (bytes, bytearray)):
            data = bytes(source)
            mime = mime_type or (guess_mime_type(name) if name else None) or sniff_mime_type(data)
        else:
            data, base = _read(source)
            name = name or base
            mime = mime_type or guess_mime_type(base) or sniff_mime_type(data)
        if not mime:
            raise ValueError("cannot tell the media type of this file; pass mime_type=")
        query = urllib.parse.urlencode({"name": name}) if name else ""
        raw = self._raw("POST", "/api/v1/blobs" + ("?" + query if query else ""), data, {"Content-Type": mime})
        body = json.loads(raw)
        return Blob(ref=body["blob_ref"], mime_type=body["mime_type"], name=body.get("name") or None, size=int(body.get("size", 0)))

    def download(self, ref: Union[str, Blob]) -> bytes:
        """The stored bytes (only for the kitchen that owns them)."""
        return self._raw("GET", f"/api/v1/blobs/{_seg(_ref(ref))}", None, {})

    def delete(self, ref: Union[str, Blob]) -> None:
        """Drop this kitchen's copy."""
        self._raw("DELETE", f"/api/v1/blobs/{_seg(_ref(ref))}", None, {})

    def _raw(self, method: str, path: str, body: Optional[bytes], headers: dict[str, str]) -> bytes:
        from agentoven.client import AgentOvenAPIError

        c = self._client
        hdrs = {**c._headers(), **headers}
        if body is None:
            hdrs.pop("Content-Type", None)
        req = urllib.request.Request(c._url + path, data=body, headers=hdrs, method=method)
        try:
            with urllib.request.urlopen(req) as resp:
                return resp.read()
        except urllib.error.HTTPError as exc:
            raw = exc.read()
            try:
                detail: Any = json.loads(raw)
            except Exception:
                detail = raw.decode(errors="replace")
            raise AgentOvenAPIError(exc.code, detail) from exc


def _ref(ref: Union[str, Blob]) -> str:
    return ref.ref if isinstance(ref, Blob) else ref


def _seg(value: str) -> str:
    return urllib.parse.quote(value, safe="")
