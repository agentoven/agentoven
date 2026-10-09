"""Provider modalities — typed config helpers, plus read models for providers
and agent cards.

A *modality* is something an agent can take in or give out beyond text:
``image``, ``pdf``, ``video``, ``audio`` (cascaded voice: speech-to-text in,
text-to-speech out) and ``realtime`` (live speech-to-speech). Modalities belong
to the **provider**: an agent has exactly the modalities of its own provider.
Text is always on and has no entry.

Nothing needs to be set — defaults come from the provider driver and the model
catalog. Use :class:`Modalities` to switch one off, or to switch image/pdf/video
on for a gateway the catalog does not know::

    from agentoven import Modalities, AudioConfig

    mods = Modalities(
        image=True,                       # shorthand for ModalityConfig(enabled=True)
        pdf=False,
        audio=AudioConfig(stt_model="whisper-1", tts_model="tts-1"),
    )
    client.create_provider("openai", "openai", api_key="sk-...", modalities=mods)

On update the object is merged per modality; use :data:`CLEAR` to delete a key
(or a whole modality) and fall back to the default::

    client.update_provider("openai", modalities=Modalities(pdf=CLEAR))

The same rules the server enforces are checked client-side, so a typo fails
before the request is sent.
"""

from __future__ import annotations

from dataclasses import dataclass, field, fields
from typing import Any, Mapping, Optional, Union

__all__ = [
    "ALL_MODALITIES",
    "CLEAR",
    "AgentCard",
    "AudioConfig",
    "ModalitiesError",
    "Modalities",
    "ModalityConfig",
    "Provider",
    "ProviderUpdate",
    "RealtimeConfig",
    "validate_modalities",
]

#: Display order of every modality, matching the server.
ALL_MODALITIES = ("text", "image", "pdf", "video", "audio", "realtime", "web")

# Keys each configurable modality accepts. "enabled" is a bool, the rest strings.
_SETTINGS = {
    "image": ("enabled",),
    "pdf": ("enabled",),
    "video": ("enabled",),
    "audio": ("enabled", "stt_model", "tts_model"),
    "realtime": ("enabled", "model"),
    "web": ("enabled",),  # built-in web search (Gemini Google Search); off until enabled
}


class ModalitiesError(ValueError):
    """Raised for an invalid modalities config or an unknown modality name."""


class _Clear:
    """Sentinel: send ``null`` so the server deletes this key (provider update only)."""

    _instance: Optional["_Clear"] = None

    def __new__(cls) -> "_Clear":
        if cls._instance is None:
            cls._instance = super().__new__(cls)
        return cls._instance

    def __repr__(self) -> str:
        return "CLEAR"

    def __bool__(self) -> bool:
        return False


#: Use as a value to delete that setting (or whole modality) on provider update.
CLEAR = _Clear()


def _check_name(name: str) -> None:
    if name in _SETTINGS:
        return
    hint = "text is always on and takes no entry; " if name == "text" else ""
    raise ModalitiesError(
        f"config.modalities.{name}: {hint}unknown modality (use image, pdf, video, audio, realtime, web)"
    )


def validate_modalities(value: Any, allow_clear: bool = False) -> dict[str, Any]:
    """Check a ``config.modalities`` mapping the way the server does and return
    it as a plain dict (a copy).

    Raises :class:`ModalitiesError` for an unknown modality, an unknown setting,
    or a value of the wrong type. ``allow_clear`` permits ``None`` (JSON null),
    which only means something on a provider *update*.
    """
    if isinstance(value, Modalities):
        return value.to_config(allow_clear=allow_clear)
    if not isinstance(value, Mapping):
        raise ModalitiesError("config.modalities must be an object")
    out: dict[str, Any] = {}
    for name in sorted(value):
        _check_name(name)
        entry = value[name]
        if entry is None or isinstance(entry, _Clear):
            if not allow_clear:
                raise ModalitiesError(f"config.modalities.{name} must be an object")
            out[name] = None
            continue
        if isinstance(entry, ModalityConfig):
            entry = entry._entry()
        if not isinstance(entry, Mapping):
            raise ModalitiesError(f"config.modalities.{name} must be an object")
        out[name] = _clean_entry(name, entry, allow_clear)
    return out


def _clean_entry(name: str, entry: Mapping[str, Any], allow_clear: bool) -> dict[str, Any]:
    allowed = _SETTINGS[name]
    clean: dict[str, Any] = {}
    for key in sorted(entry):
        if key not in allowed:
            raise ModalitiesError(
                f"config.modalities.{name}.{key}: unknown setting (allowed: {', '.join(allowed)})"
            )
        v = entry[key]
        if v is None or isinstance(v, _Clear):
            if not allow_clear:
                raise ModalitiesError(_type_message(name, key))
            clean[key] = None
        elif key == "enabled":
            if not isinstance(v, bool):
                raise ModalitiesError(_type_message(name, key))
            clean[key] = v
        else:
            if not isinstance(v, str):
                raise ModalitiesError(_type_message(name, key))
            clean[key] = v
    return clean


def _type_message(name: str, key: str) -> str:
    if key == "enabled":
        return f"config.modalities.{name}.enabled must be true or false"
    return f"config.modalities.{name}.{key} must be a string"


@dataclass
class ModalityConfig:
    """Settings for ``image``, ``pdf`` or ``video``.

    ``enabled=False`` switches the modality off; ``enabled=True`` switches it on
    for a gateway whose models the catalog does not know. ``None`` leaves it to
    the default; :data:`CLEAR` deletes a previously set value (update only).
    """

    enabled: Union[bool, None, _Clear] = None

    def _entry(self) -> dict[str, Any]:
        entry: dict[str, Any] = {}
        for f in fields(self):
            v = getattr(self, f.name)
            if v is None:
                continue
            entry[f.name] = None if isinstance(v, _Clear) else v
        return entry

    #: Which modality this config is for; the generic class stands in for
    #: image, pdf and video, which all take just ``enabled``.
    _name = "image"

    def to_config(self) -> dict[str, Any]:
        """The wire form, e.g. ``{"enabled": True}`` — type-checked."""
        return _clean_entry(self._name, self._entry(), allow_clear=True)


@dataclass
class AudioConfig(ModalityConfig):
    """Cascaded voice settings: the speech-to-text and text-to-speech models."""

    stt_model: Union[str, None, _Clear] = None
    tts_model: Union[str, None, _Clear] = None
    _name = "audio"


@dataclass
class RealtimeConfig(ModalityConfig):
    """Live speech-to-speech settings: the realtime model."""

    model: Union[str, None, _Clear] = None
    _name = "realtime"


_Entry = Union[ModalityConfig, bool, Mapping[str, Any], None, _Clear]


@dataclass
class Modalities:
    """The ``config.modalities`` section of a provider.

    Each field takes a :class:`ModalityConfig` (:class:`AudioConfig` /
    :class:`RealtimeConfig` for those two), a bare ``bool`` as shorthand for
    ``enabled``, a plain dict, :data:`CLEAR` (delete the modality on update) or
    ``None`` (leave alone).
    """

    image: _Entry = None
    pdf: _Entry = None
    video: _Entry = None
    audio: _Entry = None
    realtime: _Entry = None
    web: _Entry = None  # built-in web search; unlike the others it is off until enabled

    def to_config(self, allow_clear: bool = True) -> dict[str, Any]:
        """The value for ``config["modalities"]``, validated.

        ``allow_clear=False`` additionally refuses :data:`CLEAR` — pass it for
        a provider *create*, where there is nothing to delete.
        """
        raw: dict[str, Any] = {}
        for name in _SETTINGS:
            v = getattr(self, name)
            if v is None:
                continue
            if isinstance(v, bool):
                v = {"enabled": v}
            raw[name] = v
        return validate_modalities(raw, allow_clear=allow_clear)

    @classmethod
    def from_config(cls, config: Optional[Mapping[str, Any]]) -> "Modalities":
        """Build from a ``config["modalities"]`` mapping (validated)."""
        clean = validate_modalities(config or {}, allow_clear=True)
        kwargs: dict[str, Any] = {}
        for name, entry in clean.items():
            if entry is None:
                kwargs[name] = CLEAR
            elif name == "audio":
                kwargs[name] = AudioConfig(**entry)
            elif name == "realtime":
                kwargs[name] = RealtimeConfig(**entry)
            else:
                kwargs[name] = ModalityConfig(**entry)
        return cls(**kwargs)


def _modality_name(name: str) -> str:
    if name == "text" or name in _SETTINGS:
        return name
    raise ModalitiesError(
        f"unknown modality {name!r} (use {', '.join(ALL_MODALITIES)})"
    )


def _supports(modalities: list[str], name: str) -> bool:
    _modality_name(name)
    return name == "text" or name in modalities


@dataclass
class Provider:
    """A model provider as the control plane reports it.

    ``modalities`` is the *effective* list (``["text", "image", ...]``): the
    union over the provider's models of what the driver, the model catalog and
    ``config.modalities`` allow. The server computes it on list, get, create and
    update responses; an older server may omit it on create/update, in which case
    call :meth:`AgentOvenClient.get_provider` for the effective list.
    """

    name: str
    kind: str = ""
    endpoint: str = ""
    models: list[str] = field(default_factory=list)
    config: dict[str, Any] = field(default_factory=dict, repr=False)  # may hold secrets
    modalities: list[str] = field(default_factory=list)
    is_default: bool = False
    raw: dict[str, Any] = field(default_factory=dict, repr=False)

    @classmethod
    def from_dict(cls, data: Mapping[str, Any]) -> "Provider":
        return cls(
            name=data.get("name", ""),
            kind=data.get("kind", ""),
            endpoint=data.get("endpoint", "") or "",
            models=list(data.get("models") or []),
            config=dict(data.get("config") or {}),
            modalities=list(data.get("modalities") or []),
            is_default=bool(data.get("is_default", False)),
            raw=dict(data),
        )

    def supports(self, modality: str) -> bool:
        """Whether agents on this provider can use ``modality`` (text: always)."""
        return _supports(self.modalities, modality)

    def configured_modalities(self) -> Modalities:
        """The ``config.modalities`` overrides set on this provider."""
        return Modalities.from_config(self.config.get("modalities"))


@dataclass
class ProviderUpdate:
    """Result of :meth:`AgentOvenClient.update_provider`."""

    provider: Provider
    #: Ready agents burnt because their model was removed from the provider.
    agents_burnt: int = 0


@dataclass
class AgentCard:
    """An agent's A2A card. ``modalities`` is what its provider offers."""

    name: str
    description: str = ""
    url: str = ""
    version: str = ""
    modalities: list[str] = field(default_factory=list)
    input_modes: list[str] = field(default_factory=list)
    output_modes: list[str] = field(default_factory=list)
    capabilities: dict[str, Any] = field(default_factory=dict)
    skills: list[dict[str, Any]] = field(default_factory=list)
    raw: dict[str, Any] = field(default_factory=dict, repr=False)

    @classmethod
    def from_dict(cls, data: Mapping[str, Any]) -> "AgentCard":
        return cls(
            name=data.get("name", ""),
            description=data.get("description", "") or "",
            url=data.get("url", "") or "",
            version=data.get("version", "") or "",
            modalities=list(data.get("modalities") or []),
            input_modes=list(data.get("defaultInputModes") or []),
            output_modes=list(data.get("defaultOutputModes") or []),
            capabilities=dict(data.get("capabilities") or {}),
            skills=list(data.get("skills") or []),
            raw=dict(data),
        )

    def supports(self, modality: str) -> bool:
        """Whether the agent can use ``modality`` (``"text"`` is always True).

        ``supports("audio")`` means cascaded voice via ``invoke(audio=...)``;
        ``supports("realtime")`` means a live ``client.realtime(...)`` call.
        """
        return _supports(self.modalities, modality)
