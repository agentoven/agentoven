"""Every call the Python client makes names a (verb, path) the server has.

sdk/fixtures/server-routes.json is the server's route list, generated from its chi tree (agentoven-pro's
internal/routeguard route test, with UPDATE_SDK_ROUTES=1). This test reads the client's source for each call
and fails for one the server does not serve: a PUT where the server wants PATCH, a path that was never a
route, a method for a route that was removed. Review 3 found six of those, found by hand; this is what finds
the next one in the build.

It reads the call sites, so a path built in a variable is not seen: write the path as a literal at the call.
"""

import json
import re
from pathlib import Path

ROOT = Path(__file__).resolve().parents[1]
FIXTURE = ROOT.parent / "fixtures" / "server-routes.json"
SOURCES = sorted((ROOT / "agentoven").glob("*.py"))

VERBS = "get|post|put|delete|patch"
# self._get("/path"), client._put(f"/path/{x}", ...)
SHORTHAND = re.compile(rf'\._(?P<verb>{VERBS})\(\s*f?"(?P<path>/[^"]*)"')
# self._request("PATCH", "/path"), self._request_text("GET", f"/path")
EXPLICIT = re.compile(r'_request(?:_text)?\(\s*"(?P<verb>GET|POST|PUT|DELETE|PATCH)"\s*,\s*f?"(?P<path>/[^"]*)"')


# Reads that go through a helper with the path as its argument: the reference checks' catalog reads
# (refcheck.load_catalog: part("tools", "/api/v1/tools"), _read(client, "/api/v1/scenarios")) and the stored
# agent or recipe a validate=True check reads (self._stored(f"/api/v1/agents/{...}")). They are all GETs.
HELPER_READS = re.compile(r'(?:\bpart\(\s*"[a-z]+"\s*,|\b_read\(\s*client\s*,|\._stored\()\s*f?"(?P<path>/[^"]*)"')


def normalize(path: str) -> str:
    path = re.sub(r"\{[^}]*\}", "{}", path)  # parameter names and f-string expressions do not matter
    path = path.split("?")[0]
    path = re.sub(r"(?<=[^/])\{\}$", "", path)  # "/api/v1/traces{qs}": the placeholder was a query string
    return path.rstrip("/") or "/"


def server_routes() -> set:
    data = json.loads(FIXTURE.read_text())
    return {(r["method"], normalize(r["path"])) for r in data["routes"]}


def client_calls() -> list:
    calls = []
    for src in SOURCES:
        text = src.read_text()
        for rx in (SHORTHAND, EXPLICIT, HELPER_READS):
            for m in rx.finditer(text):
                line = text.count("\n", 0, m.start()) + 1
                verb = m.group("verb").upper() if "verb" in rx.groupindex else "GET"
                calls.append((src.name, line, verb, m.group("path")))
    return calls


def test_the_scanner_sees_the_clients_calls():
    # A scanner that matched nothing would pass every contract: hold it to the size of what it must see.
    assert len(client_calls()) > 150


def test_every_call_the_client_makes_is_a_route_the_server_has():
    routes = server_routes()
    missing = [
        f"{name}:{line} {verb} {path}"
        for name, line, verb, path in client_calls()
        if (verb, normalize(path)) not in routes
    ]
    assert not missing, "calls the server has no route for:\n  " + "\n  ".join(missing)


def test_the_reference_check_routes_are_called_and_exist():
    # validate routes (agent_validate / recipe_validate) and the lists load_catalog reads must be seen by the scanner,
    # or a rename of one would pass this contract unnoticed.
    seen = {(verb, normalize(path)) for _, _, verb, path in client_calls()}
    routes = server_routes()
    wanted = {("GET", "/api/v1/agents/{}/validate"), ("GET", "/api/v1/recipes/{}/validate"),
              ("POST", "/api/v1/agents"), ("PUT", "/api/v1/agents/{}")}
    wanted |= {("GET", f"/api/v1/{p}") for p in ("models/providers", "prompts", "tools", "skills", "agents", "recipes",
                                                  "channels", "environments", "credentials", "scenarios")}
    assert wanted <= seen, f"not called by the client: {sorted(wanted - seen)}"
    assert wanted <= routes, f"not routes of the server: {sorted(wanted - routes)}"
