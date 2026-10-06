"""Media helpers — attachments for session messages and cascaded voice.

Attachments (images, PDFs, audio, video) become typed ``content_parts`` on a
session message::

    from agentoven import Attachment

    reply = client.send_session_message(
        "vision-agent", session_id,
        "What is in these?",
        attachments=[Attachment.from_path("chart.png"), Attachment.from_path("report.pdf")],
    )

Cascaded voice (speech in, speech out) goes through ``invoke``::

    result = client.invoke("support-agent", audio=AudioInput.from_path("question.wav"),
                           voice_output=True, voice="alloy")
    print(result.transcript, result.response)
    result.save_audio("answer.mp3")

Every part carries a MIME type — the server needs it to pick the provider wire
format. It is inferred from the file name, URL or the bytes' magic number; pass
``mime_type=`` when it cannot be (a bare ``bytes`` of an unknown format).
"""

from __future__ import annotations

import base64
import mimetypes
import os
from dataclasses import dataclass, field
from pathlib import Path
from typing import Any, Optional, Union

__all__ = [
    "Attachment",
    "AudioClip",
    "AudioInput",
    "InvokeResult",
    "guess_mime_type",
    "sniff_mime_type",
]

# mimetypes disagrees with the server's accepted spellings on a few formats
# (.wav is audio/x-wav on some platforms), so the common ones are pinned.
_EXT_MIME = {
    ".png": "image/png",
    ".jpg": "image/jpeg",
    ".jpeg": "image/jpeg",
    ".gif": "image/gif",
    ".webp": "image/webp",
    ".pdf": "application/pdf",
    ".wav": "audio/wav",
    ".mp3": "audio/mpeg",
    ".m4a": "audio/mp4",
    ".ogg": "audio/ogg",
    ".oga": "audio/ogg",
    ".flac": "audio/flac",
    ".mp4": "video/mp4",
    ".mov": "video/quicktime",
    ".mpeg": "video/mpeg",
    ".mpg": "video/mpeg",
    ".avi": "video/x-msvideo",
}
# .webm is both an audio and a video container; the caller's intent decides.
_WEBM = {"audio": "audio/webm", "video": "video/webm"}

_AUDIO_MP3_FRAME = (b"\xff\xfb", b"\xff\xf3", b"\xff\xf2")


def sniff_mime_type(data: bytes, prefer: str = "") -> Optional[str]:
    """Identify common media formats from their leading bytes, or ``None``.

    ``prefer`` (``"audio"`` or ``"video"``) breaks the WebM tie.
    """
    head = data[:16]
    if head.startswith(b"\x89PNG\r\n\x1a\n"):
        return "image/png"
    if head.startswith(b"\xff\xd8\xff"):
        return "image/jpeg"
    if head.startswith((b"GIF87a", b"GIF89a")):
        return "image/gif"
    if head[:4] == b"RIFF" and head[8:12] == b"WEBP":
        return "image/webp"
    if head.startswith(b"%PDF"):
        return "application/pdf"
    if head[:4] == b"RIFF" and head[8:12] == b"WAVE":
        return "audio/wav"
    if head[:4] == b"RIFF" and head[8:12] == b"AVI ":
        return "video/x-msvideo"
    if head.startswith(b"ID3") or head[:2] in _AUDIO_MP3_FRAME:
        return "audio/mpeg"
    if head.startswith(b"OggS"):
        return "audio/ogg"
    if head.startswith(b"fLaC"):
        return "audio/flac"
    if head.startswith(b"\x1a\x45\xdf\xa3"):
        return _WEBM.get(prefer, "video/webm")
    if head[4:8] == b"ftyp":
        brand = head[8:12]
        if brand in (b"M4A ", b"M4B "):
            return "audio/mp4"
        if brand == b"qt  ":
            return "video/quicktime"
        return "audio/mp4" if prefer == "audio" else "video/mp4"
    return None


def guess_mime_type(name: str, prefer: str = "") -> Optional[str]:
    """Guess a MIME type from a file name or URL path, or ``None``."""
    path = name.split("?", 1)[0].split("#", 1)[0]
    ext = os.path.splitext(path)[1].lower()
    if ext == ".webm":
        return _WEBM.get(prefer, "video/webm")
    if ext in _EXT_MIME:
        return _EXT_MIME[ext]
    guessed, _ = mimetypes.guess_type(path)
    return guessed


def _normalize_mime(mime: str) -> str:
    """Drop parameters (``audio/webm;codecs=opus`` -> ``audio/webm``): the server
    matches the bare type."""
    return mime.split(";", 1)[0].strip().lower()


def _part_type(mime: str) -> str:
    if mime.startswith("image/"):
        return "image"
    if mime.startswith("audio/"):
        return "audio"
    if mime.startswith("video/"):
        return "video"
    return "file"  # PDFs and other documents


def _b64(data: bytes) -> str:
    return base64.b64encode(data).decode("ascii")


def _read(path: Union[str, "os.PathLike[str]"]) -> tuple[bytes, str]:
    p = Path(path)
    try:
        return p.read_bytes(), p.name
    except OSError as exc:
        raise ValueError(f"cannot read {str(path)!r}: {exc.strerror or exc}") from exc


@dataclass
class Attachment:
    """One media part of a session message: an image, PDF, audio clip or video.

    Build it with :meth:`from_path`, :meth:`from_bytes` or :meth:`from_url`.
    Audio attachments are transcribed by the server (the agent's provider must
    offer the ``audio`` modality); images, PDFs and video go to the model, so
    the provider must offer ``image`` / ``pdf`` / ``video``.
    """

    mime_type: str
    data: Optional[bytes] = field(default=None, repr=False)
    url: Optional[str] = None
    name: Optional[str] = None

    def __post_init__(self) -> None:
        if not self.mime_type:
            raise ValueError("an attachment needs a mime_type")
        self.mime_type = _normalize_mime(self.mime_type)
        if (self.data is None) == (self.url is None):
            raise ValueError("an attachment needs exactly one of data or url")

    @property
    def type(self) -> str:
        """The content part type: ``image``, ``audio``, ``video`` or ``file`` (PDF)."""
        return _part_type(self.mime_type)

    @classmethod
    def from_bytes(cls, data: bytes, mime_type: Optional[str] = None, name: Optional[str] = None) -> "Attachment":
        mime = mime_type or (guess_mime_type(name) if name else None) or sniff_mime_type(data)
        if not mime:
            raise ValueError("cannot tell the media type from these bytes; pass mime_type=")
        return cls(mime_type=mime, data=bytes(data), name=name)

    @classmethod
    def from_path(cls, path: Union[str, "os.PathLike[str]"], mime_type: Optional[str] = None) -> "Attachment":
        data, name = _read(path)
        mime = mime_type or guess_mime_type(name) or sniff_mime_type(data)
        if not mime:
            raise ValueError(f"cannot tell the media type of {name!r}; pass mime_type=")
        return cls(mime_type=mime, data=data, name=name)

    @classmethod
    def from_url(cls, url: str, mime_type: Optional[str] = None, name: Optional[str] = None) -> "Attachment":
        mime = mime_type or guess_mime_type(url)
        if not mime:
            raise ValueError(f"cannot tell the media type of {url!r} from its extension; pass mime_type=")
        return cls(mime_type=mime, url=url, name=name)

    def to_content_part(self) -> dict[str, Any]:
        """The ``content_parts`` entry for a session message."""
        media: dict[str, Any] = {"mime_type": self.mime_type}
        if self.data is not None:
            media["data"] = _b64(self.data)
        else:
            media["url"] = self.url
        if self.name:
            media["name"] = self.name
        return {"type": self.type, "media": media}


@dataclass
class AudioInput:
    """Spoken input for ``invoke(audio=...)``: bytes or base64 plus a MIME type
    (``audio/wav``, ``audio/mpeg``, ``audio/mp4``, ``audio/webm``, ``audio/ogg``
    or ``audio/flac``)."""

    data: str  # base64
    mime_type: str

    def __post_init__(self) -> None:
        if not self.data:
            raise ValueError("audio data is empty")
        if not self.mime_type:
            raise ValueError("audio needs a mime_type")
        self.mime_type = _normalize_mime(self.mime_type)
        if not self.mime_type.startswith("audio/"):
            raise ValueError(f"audio mime_type must be audio/*, got {self.mime_type!r}")

    @classmethod
    def from_bytes(cls, data: bytes, mime_type: Optional[str] = None) -> "AudioInput":
        mime = mime_type or sniff_mime_type(data, prefer="audio")
        if not mime:
            raise ValueError("cannot tell the audio format from these bytes; pass mime_type=")
        return cls(data=_b64(data), mime_type=mime)

    @classmethod
    def from_path(cls, path: Union[str, "os.PathLike[str]"], mime_type: Optional[str] = None) -> "AudioInput":
        data, name = _read(path)
        mime = mime_type or guess_mime_type(name, prefer="audio") or sniff_mime_type(data, prefer="audio")
        if not mime:
            raise ValueError(f"cannot tell the audio format of {name!r}; pass mime_type=")
        return cls(data=_b64(data), mime_type=mime)

    @classmethod
    def from_base64(cls, data: str, mime_type: str) -> "AudioInput":
        return cls(data=data, mime_type=mime_type)

    @classmethod
    def coerce(
        cls,
        audio: Union["AudioInput", bytes, str, "os.PathLike[str]"],
        mime_type: Optional[str] = None,
    ) -> "AudioInput":
        """Accept what ``invoke(audio=...)`` takes: an :class:`AudioInput`, raw
        ``bytes``, or a file path (``str`` / ``Path``). For base64 text use
        :meth:`from_base64`."""
        if isinstance(audio, AudioInput):
            return audio
        if isinstance(audio, (bytes, bytearray, memoryview)):
            return cls.from_bytes(bytes(audio), mime_type)
        if isinstance(audio, (str, os.PathLike)):
            return cls.from_path(audio, mime_type)
        raise TypeError("audio must be an AudioInput, bytes, or a file path")

    def to_wire(self) -> dict[str, str]:
        return {"data": self.data, "mime_type": self.mime_type}


_AUDIO_EXT = {
    "audio/mpeg": ".mp3",
    "audio/mp3": ".mp3",
    "audio/wav": ".wav",
    "audio/x-wav": ".wav",
    "audio/mp4": ".m4a",
    "audio/ogg": ".ogg",
    "audio/webm": ".webm",
    "audio/flac": ".flac",
    "audio/aac": ".aac",
    "audio/opus": ".opus",
}


@dataclass
class AudioClip:
    """Audio returned by the server (spoken reply), decoded."""

    data: bytes = field(repr=False)
    mime_type: str = ""

    @property
    def extension(self) -> str:
        """A file extension for the clip, e.g. ``.mp3`` (``.bin`` if unknown)."""
        return _AUDIO_EXT.get(_normalize_mime(self.mime_type), ".bin")

    def save(self, path: Union[str, "os.PathLike[str]"]) -> Path:
        """Write the clip to ``path`` and return it. If ``path`` has no suffix
        the right one for the clip's format is added."""
        p = Path(path)
        if not p.suffix:
            p = p.with_suffix(self.extension)
        p.write_bytes(self.data)
        return p

    @classmethod
    def from_wire(cls, obj: dict[str, Any]) -> "AudioClip":
        return cls(data=base64.b64decode(obj.get("data") or ""), mime_type=obj.get("mime_type", ""))


@dataclass
class InvokeResult:
    """The reply from ``invoke``.

    ``transcript`` is what the server heard when you sent ``audio``; ``audio``
    is the spoken reply when you asked for ``voice_output``.
    """

    response: str = ""
    agent: str = ""
    trace_id: str = ""
    session_id: str = ""
    turns: int = 0
    usage: dict[str, Any] = field(default_factory=dict)
    latency_ms: int = 0
    transcript: Optional[str] = None
    audio: Optional[AudioClip] = None
    raw: dict[str, Any] = field(default_factory=dict, repr=False)

    @classmethod
    def from_dict(cls, data: dict[str, Any]) -> "InvokeResult":
        audio = data.get("audio")
        return cls(
            response=data.get("response", ""),
            agent=data.get("agent", ""),
            trace_id=data.get("trace_id", ""),
            session_id=data.get("session_id", "") or "",
            turns=int(data.get("turns", 0) or 0),
            usage=dict(data.get("usage") or {}),
            latency_ms=int(data.get("latency_ms", 0) or 0),
            transcript=data.get("transcript"),
            audio=AudioClip.from_wire(audio) if isinstance(audio, dict) else None,
            raw=data,
        )

    def save_audio(self, path: Union[str, "os.PathLike[str]"]) -> Path:
        """Save the spoken reply. Raises ``ValueError`` if the call did not ask
        for ``voice_output`` (or the server returned no audio)."""
        if self.audio is None:
            raise ValueError("this reply has no audio; call invoke(..., voice_output=True)")
        return self.audio.save(path)
