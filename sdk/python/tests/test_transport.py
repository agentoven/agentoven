"""The transport's defences: a request that cannot hang, a redirect that cannot carry the key elsewhere,
and a name that cannot choose another route (review 3, findings 14 and 15)."""

import socket
import threading
import time
from http.server import BaseHTTPRequestHandler, ThreadingHTTPServer

import pytest

from agentoven import AgentOvenClient
from agentoven.client import AgentOvenAPIError, DEFAULT_TIMEOUT


def test_a_server_that_goes_quiet_ends_in_a_timeout():
    quiet = socket.socket()
    quiet.bind(("127.0.0.1", 0))
    quiet.listen(5)  # accepts the connection at the kernel level and never answers
    try:
        client = AgentOvenClient(url=f"http://127.0.0.1:{quiet.getsockname()[1]}", timeout=0.4)
        started = time.time()
        with pytest.raises(Exception):
            client.list_guardrails()
        assert time.time() - started < 5
    finally:
        quiet.close()


def test_the_default_timeout_is_finite_and_configurable(monkeypatch):
    assert 0 < DEFAULT_TIMEOUT < 3600
    assert AgentOvenClient()._timeout == DEFAULT_TIMEOUT
    monkeypatch.setenv("AGENTOVEN_TIMEOUT", "7")
    assert AgentOvenClient()._timeout == 7.0
    assert AgentOvenClient(timeout=3)._timeout == 3.0
    assert AgentOvenClient(timeout=3).with_kitchen("k")._timeout == 3.0
    monkeypatch.setenv("AGENTOVEN_TIMEOUT", "soon")
    with pytest.raises(ValueError):
        AgentOvenClient()
    with pytest.raises(ValueError):
        AgentOvenClient(timeout=0)


def test_a_redirect_is_refused_and_the_key_goes_nowhere_else():
    elsewhere = []

    class Target(BaseHTTPRequestHandler):
        def do_GET(self):
            elsewhere.append(self.headers.get("Authorization"))
            self.send_response(200)
            self.end_headers()

        log_message = lambda *a: None  # noqa: E731

    target = ThreadingHTTPServer(("127.0.0.1", 0), Target)
    threading.Thread(target=target.serve_forever, daemon=True).start()

    class Redirector(BaseHTTPRequestHandler):
        def do_GET(self):
            self.send_response(302)
            self.send_header("Location", f"http://127.0.0.1:{target.server_address[1]}/stolen")
            self.end_headers()

        log_message = lambda *a: None  # noqa: E731

    redirector = ThreadingHTTPServer(("127.0.0.1", 0), Redirector)
    threading.Thread(target=redirector.serve_forever, daemon=True).start()
    try:
        client = AgentOvenClient(url=f"http://127.0.0.1:{redirector.server_address[1]}", api_key="ao_secret")
        with pytest.raises(AgentOvenAPIError) as err:
            client.list_guardrails()
        assert "redirect" in str(err.value)
        time.sleep(0.2)
        assert elsewhere == [], "the credential must not reach the redirect target"
    finally:
        redirector.shutdown()
        target.shutdown()


def test_an_id_cannot_walk_to_another_route(api):
    client = AgentOvenClient(url=api.url)
    api.reply("GET", "/api/v1/users/..%2F..%2Fagents", {"id": "x"})
    client.get_user("../../agents")
    assert api.last["path"] == "/api/v1/users/..%2F..%2Fagents"
