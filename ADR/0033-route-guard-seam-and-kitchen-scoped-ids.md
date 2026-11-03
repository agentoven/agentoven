# ADR-0033: The route guard seam and ids that belong to a kitchen

**Status:** Accepted (2026-11-03)
**Scope:** OSS control plane (`internal/api/router.go`, `internal/api/handlers`, `internal/router`, `internal/integrations`, `internal/store`); the roles themselves are Pro ([agentoven-pro ADR-0040](../../agentoven-pro/ADR/0040-head-chef-and-restaurateur-roles.md) addendum).

## Context

The community edition has no role model: any caller the auth chain lets in may call any route. Pro's roles were enforced only on the routes Pro registers, so on the community routes (agents, tools, providers, tasks, sessions, recipes, approvals, traces) a Viewer could do what a Head Chef does, including repointing a provider so its credential was sent to another host. Separately, several routes loaded a record by an id in the URL and compared nothing with the request's kitchen.

## Decision

1. **`Handlers.RouteGuard`**: an optional middleware (`func(http.Handler) http.Handler`) the router runs on every request after authentication and CORS. The community edition installs none, so its behaviour is unchanged. An edition with roles installs one that maps method and route to a permission; it must be able to tell which route a request reaches (chi 5.2 `Mux.Find`, on `RawPath` when set, as chi routes), which is why it lives in the edition and not here.
2. **`Handlers.ProviderRepoint`**: an optional check that decides whether a caller may point a provider that holds a credential at a different scheme, host or port without entering a new credential in the same request. Absent, anyone who may update a provider may (community behaviour). Present and false, `PUT /api/v1/models/providers/{p}` answers `403 provider_endpoint_pinned`. A reference to a stored secret is not a new credential.
3. **Provider clients refuse a redirect to another host** (`router.refuseCrossHostRedirect`): Go forwards custom headers such as `x-api-key` on a redirect.
4. **An id names a record of one kitchen.** Recipe runs (get, cancel, rerun, gates), approvals (get; list uses the authenticated kitchen), sessions (get, delete, message, and the agent in the URL) compare the record's kitchen with `middleware.GetKitchen` and answer 404 otherwise (`handlers/kitchen_scope.go`). The LangFuse, LangChain and PicoClaw adapters take the kitchen from the authenticated request, never the raw header or a body field. `ListApprovals` with an empty kitchen is `ErrKitchenRequired`. `TestByIDRoutesAreKitchenScoped` walks the route tree and fails for a route with a path parameter that is not classified.
5. **A2A**: request bodies are bounded (32 MiB); the guardrails also judge data parts, file names, text files and parts of an unknown kind (`handlers/a2a_parts.go`); a message that would need a redaction inside those parts is refused rather than forwarded unredacted.
6. **LangFuse ingest** is bounded (4 MiB, 1000 events).

## Not done

- The memory store's list functions still treat an empty kitchen as every kitchen (the router's provider scan relies on it).
- Task and blob lookups were already scoped; they stay classified by name in the route test, with their own tests.
- Imported LangFuse traces keep their input and output in `Metadata`, which trace-content withholding does not yet blank (the trace serializers are another change).
