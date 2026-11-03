import json
import threading
from http.server import BaseHTTPRequestHandler, ThreadingHTTPServer

import pytest

from agentoven import AgentOvenAPIError, AgentOvenClient, Attachment, Documents
from agentoven.blobs import Blob
from agentoven.media import guess_mime_type

REF = "ab" * 32
DOCX = "application/vnd.openxmlformats-officedocument.wordprocessingml.document"


class _Fake(BaseHTTPRequestHandler):
    seen = []
    status = 200
    reply = {"format": "docx", "text": "hello", "blocks": []}

    def log_message(self, *a):
        pass

    def do_POST(self):
        n = int(self.headers.get("Content-Length") or 0)
        body = self.rfile.read(n) if n else b""
        type(self).seen.append({"path": self.path, "ctype": self.headers.get("Content-Type"), "kitchen": self.headers.get("X-Kitchen"),
                                "auth": self.headers.get("Authorization"), "body": body})
        self.send_response(type(self).status)
        self.send_header("Content-Type", "application/json")
        self.end_headers()
        self.wfile.write(json.dumps(type(self).reply).encode())


@pytest.fixture
def docs():
    _Fake.seen, _Fake.status, _Fake.reply = [], 200, {"format": "docx", "text": "hello", "blocks": []}
    srv = ThreadingHTTPServer(("127.0.0.1", 0), _Fake)
    threading.Thread(target=srv.serve_forever, daemon=True).start()
    yield AgentOvenClient(url=f"http://127.0.0.1:{srv.server_address[1]}", api_key="k", kitchen="claims").documents
    srv.shutdown()


def last_json():
    return json.loads(_Fake.seen[-1]["body"])


def test_the_client_exposes_documents(docs):
    assert isinstance(docs, Documents)


def test_extract_of_a_stored_file_sends_only_its_reference(docs):
    out = docs.extract(Blob(ref=REF, mime_type=DOCX, name="c.docx"), max_text_bytes=5000)
    req = _Fake.seen[0]
    assert req["path"] == "/api/v1/documents/extract" and req["ctype"] == "application/json"
    assert req["kitchen"] == "claims" and req["auth"] == "Bearer k"
    assert last_json() == {"blob_ref": REF, "max_text_bytes": 5000}
    assert out["text"] == "hello"
    docs.extract(blob_ref=REF)
    assert last_json() == {"blob_ref": REF}


def test_extract_of_a_path_or_bytes_uploads_the_file_for_this_call_only(docs, tmp_path):
    f = tmp_path / "claim.docx"
    f.write_bytes(b"PK\x03\x04zip")
    docs.extract(f)
    req = _Fake.seen[-1]
    assert req["path"] == "/api/v1/documents/extract?name=claim.docx" and req["ctype"] == DOCX and req["body"] == b"PK\x03\x04zip"
    docs.extract(b"a,b\n1,2\n", name="lines.csv")
    assert _Fake.seen[-1]["ctype"] == "text/csv" and _Fake.seen[-1]["body"] == b"a,b\n1,2\n"
    docs.extract(Attachment.from_bytes(b"x", mime_type="text/plain", name="n.txt"))
    assert _Fake.seen[-1]["ctype"] == "text/plain"


def test_extract_refuses_what_it_cannot_send(docs):
    with pytest.raises(ValueError, match="give a file"):
        docs.extract()
    with pytest.raises(ValueError, match="no bytes"):
        docs.extract(Attachment.from_url("https://example.com/x.pdf"))
    with pytest.raises(ValueError, match="media type"):
        docs.extract(b"opaque bytes")
    with pytest.raises(ValueError, match="max_text_bytes"):
        docs.extract(b"x", name="a.txt", max_text_bytes=10)
    assert _Fake.seen == []


def test_ocr_names_the_provider_and_the_second_pass(docs):
    _Fake.reply = {"text": "[Page 1]\nx", "confidence": "unverified"}
    out = docs.ocr(REF, provider="vision", model="gpt-4o", max_pages=3, verify={"provider": "vision", "model": "other", "min_agreement": 0.9})
    assert _Fake.seen[0]["path"] == "/api/v1/documents/ocr"
    assert last_json() == {"blob_ref": REF, "provider": "vision", "model": "gpt-4o", "max_pages": 3,
                           "verify": {"provider": "vision", "model": "other", "min_agreement": 0.9}}
    assert out["confidence"] == "unverified"
    docs.ocr(Blob(ref=REF, mime_type="image/png"), provider="vision")
    assert last_json() == {"blob_ref": REF, "provider": "vision"}


def test_extract_fields_from_a_blob_or_text(docs):
    schema = {"type": "object", "properties": {"total": {"type": "number"}}}
    _Fake.reply = {"fields": [{"name": "total", "status": "grounded"}], "all_grounded": True}
    out = docs.extract_fields(REF, schema, provider="vision", model="m", instructions="amounts in USD", ocr={})
    assert _Fake.seen[0]["path"] == "/api/v1/documents/fields"
    assert last_json() == {"schema": schema, "provider": "vision", "blob_ref": REF, "model": "m", "instructions": "amounts in USD", "ocr": {}}
    assert out["all_grounded"] is True
    docs.extract_fields(schema=schema, provider="vision", text="Total 5")
    assert last_json() == {"schema": schema, "provider": "vision", "text": "Total 5"}
    with pytest.raises(ValueError, match="exactly one"):
        docs.extract_fields(REF, schema, provider="vision", text="x")
    with pytest.raises(ValueError, match="exactly one"):
        docs.extract_fields(schema=schema, provider="vision")
    with pytest.raises(ValueError, match="schema"):
        docs.extract_fields(REF, provider="vision")


def test_errors_keep_their_meaning(docs):
    _Fake.status, _Fake.reply = 422, {"error": "the document has no text layer"}
    with pytest.raises(AgentOvenAPIError) as e:
        docs.extract(blob_ref=REF)
    assert e.value.status_code == 422
    _Fake.status, _Fake.reply = 404, {"error": "blob not found"}
    with pytest.raises(AgentOvenAPIError) as e:
        docs.ocr(REF, provider="vision")
    assert e.value.status_code == 404


def test_document_extensions_have_media_types():
    assert guess_mime_type("claim.DOCX") == DOCX
    assert guess_mime_type("costs.xlsx").endswith("spreadsheetml.sheet")
    for name, mime in {"a.csv": "text/csv", "a.tsv": "text/tab-separated-values", "a.md": "text/markdown", "a.rtf": "application/rtf", "a.html": "text/html"}.items():
        assert guess_mime_type(name) == mime


def test_a_stored_document_is_a_file_part(docs):
    assert Blob(ref=REF, mime_type=DOCX, name="c.docx").to_content_part()["type"] == "file"
