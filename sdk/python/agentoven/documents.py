"""Read what a file says, transcribe scans, and pull fields out with the quote they came from.

    docs = client.documents                      # agentoven.documents.Documents(client)

    doc = docs.extract("claim.docx")             # path, bytes, Attachment or Blob; read in-process, no model
    print(doc["text"])                           # headings, lists, tables as markdown; [Sheet: ...]/[Page N] labels
    docs.extract(blob)                           # a file already uploaded (client.blobs.upload)

    scan = docs.ocr(blob, provider="vision")     # an image or a scanned PDF, page by page, through your provider
    scan["confidence"]                           # "unverified" unless verify={...} asked for a second model

    docs.extract_fields(blob, schema, provider="vision")   # every field comes back with its quote

``extract`` reads Word, Excel, HTML, CSV/TSV, Markdown, RTF, plain text and the text layer of a PDF. A page
with no text layer is listed in ``empty_pages`` and ``needs_ocr`` is true: it is not returned as blank.
``ocr`` and ``extract_fields`` send the document to the one provider you name, through the control plane's
router, so budgets, rate limits and traces apply; a refused budget raises :class:`BudgetExceededError` and
a rate limit :class:`RateLimitedError`. ``confidence`` says what was checked, not a probability; and a field
whose ``status`` is ``ungrounded`` has a quote that is not in the document, so do not use its value unseen.
"""

from __future__ import annotations

import os
import urllib.parse
from typing import TYPE_CHECKING, Any, Mapping, Optional, Union

from agentoven.blobs import Blob
from agentoven.media import Attachment, _read, guess_mime_type, sniff_mime_type

if TYPE_CHECKING:  # pragma: no cover
    from agentoven.client import AgentOvenClient

Source = Union[str, "os.PathLike[str]", bytes, Attachment, Blob]


class Documents:
    """``POST /api/v1/documents/{extract,ocr,fields}`` for one client."""

    def __init__(self, client: "AgentOvenClient") -> None:
        self._client = client

    def extract(
        self,
        source: Optional[Source] = None,
        *,
        blob_ref: Optional[str] = None,
        mime_type: Optional[str] = None,
        name: Optional[str] = None,
        max_text_bytes: Optional[int] = None,
    ) -> dict[str, Any]:
        """Read a file into text and structure.

        Give a :class:`Blob` or ``blob_ref=`` for a file already stored (nothing is uploaded), or a path, bytes
        or an :class:`Attachment` built from bytes, which is sent for this call only and not stored. Returns
        ``{"format", "text", "blocks", "pages", "sheets", "warnings", "truncated", "needs_ocr", "empty_pages", ...}``.
        """
        ref = blob_ref or (source.ref if isinstance(source, Blob) else None)
        if ref:
            body: dict[str, Any] = {"blob_ref": ref}
            if max_text_bytes:
                body["max_text_bytes"] = max_text_bytes
            return self._client._request("POST", "/api/v1/documents/extract", body)
        if source is None:
            raise ValueError("give a file (path, bytes, Attachment or Blob) or blob_ref=")
        if max_text_bytes:
            raise ValueError("max_text_bytes applies to a stored file (blob_ref); an upload is cut at the server's default")
        data, mime, name = _resolve(source, mime_type, name)
        query = urllib.parse.urlencode({"name": name}) if name else ""
        return self._client._request_bytes("POST", "/api/v1/documents/extract" + ("?" + query if query else ""), data, mime)

    def ocr(
        self,
        blob: Union[str, Blob],
        *,
        provider: str,
        model: Optional[str] = None,
        max_pages: Optional[int] = None,
        verify: Optional[Mapping[str, Any]] = None,
    ) -> dict[str, Any]:
        """Transcribe an image or a scanned PDF, one page per model call, with the provider you name.

        ``verify={"provider": ..., "model": ..., "min_agreement": 0.97}`` reads every page a second time with a
        different model: ``confidence`` becomes ``agreed`` or ``disputed``. Without it the honest value is
        ``unverified``. Pages that have a text layer are used as they are. ``incomplete`` is true when a page was
        not read (a page bound, a failure, a scan this reader cannot extract); ``warnings`` say which.
        """
        body: dict[str, Any] = {"blob_ref": _ref(blob), "provider": provider}
        if model:
            body["model"] = model
        if max_pages:
            body["max_pages"] = max_pages
        if verify:
            body["verify"] = dict(verify)
        return self._client._request("POST", "/api/v1/documents/ocr", body)

    def extract_fields(
        self,
        blob: Union[None, str, Blob] = None,
        schema: Optional[Mapping[str, Any]] = None,
        *,
        provider: str,
        text: Optional[str] = None,
        model: Optional[str] = None,
        instructions: Optional[str] = None,
        ocr: Optional[Mapping[str, Any]] = None,
    ) -> dict[str, Any]:
        """Read the properties of a flat JSON Schema out of a document (or ``text=``).

        Each field comes back as ``{"name", "value", "status", "page", "quote", "flags"}``; the server checks
        every quote against the document's own text. ``status`` is ``grounded``, ``ungrounded`` (``flags`` say
        why: ``quote_not_found``, ``value_not_in_quote``, ``wrong_type``...) or ``absent``. ``all_grounded`` is
        true only when every field asked for is grounded. ``ocr={}`` (or with its own provider/model) reads a
        scan first.
        """
        if schema is None:
            raise ValueError("schema is required")
        if (blob is None) == (text is None):
            raise ValueError("give exactly one of blob and text")
        body: dict[str, Any] = {"schema": dict(schema), "provider": provider}
        if blob is not None:
            body["blob_ref"] = _ref(blob)
        else:
            body["text"] = text
        if model:
            body["model"] = model
        if instructions:
            body["instructions"] = instructions
        if ocr is not None:
            body["ocr"] = dict(ocr)
        return self._client._request("POST", "/api/v1/documents/fields", body)


def _ref(blob: Union[str, Blob]) -> str:
    return blob.ref if isinstance(blob, Blob) else blob


def _resolve(source: Source, mime_type: Optional[str], name: Optional[str]) -> tuple[bytes, str, Optional[str]]:
    if isinstance(source, Attachment):
        if source.data is None:
            raise ValueError("an attachment built from a URL has no bytes to send")
        return source.data, mime_type or source.mime_type, name or source.name
    if isinstance(source, (bytes, bytearray)):
        data = bytes(source)
        mime = mime_type or (guess_mime_type(name) if name else None) or sniff_mime_type(data)
    else:
        data, base = _read(source)
        name = name or base
        mime = mime_type or guess_mime_type(base) or sniff_mime_type(data)
    if not mime:
        raise ValueError("cannot tell the media type of this file; pass mime_type=")
    return data, mime, name
