# ServiceNow tool pack (Pro)

The `servicenow` pack gives agents governed access to a ServiceNow instance: incidents, change requests, users and groups, configuration items and knowledge articles. It is a **curated pack**: written by hand against ServiceNow's documented Table API and Knowledge Management API, with every tool's risk class, schema, paging and error handling declared in its manifest (see [curated-tool-pack-format.md](curated-tool-pack-format.md)). Read [tool-packs.md](tool-packs.md) first for the parts every pack shares: approval, pinned credentials, the network allowlist.

> Status: tested against in-process fakes that mimic the documented API shapes (`internal/toolpacks/vendorfake`). **No ServiceNow instance has been used yet.** The list of what a real instance must confirm is at the end of this page and in Pro ADR-0061.

## The address is fixed

You give the pack your **instance** name, and it connects to `https://<instance>.service-now.com` and nowhere else (`environment: government` selects `<instance>.servicenowservices.com`). The instance name is only the part before the domain: letters, digits and hyphens, no dot, slash, colon or at sign, so it cannot be made to point at another host. Nothing else sets the address; an installer cannot supply a URL. (An operator can put a gateway in front with `AGENTOVEN_CURATED_PACK_ENDPOINTS`, see the format page.)

## Tools

| Tool | Risk | Does |
|---|---|---|
| `incident_get` | read | One incident by `sys_id` or number (`INC0010001`) |
| `incident_search` | read | Filters (text, state, priority, group, assignee, caller, CI, opened after or before), ordered, paged by `offset` |
| `incident_create` | write | New incident; returns its number, `sys_id` and a link |
| `incident_update` | write | Change state, urgency, impact, assignment, resolution fields |
| `incident_add_work_note` | write | An internal work note (`work_notes`); never the customer-visible `comments` |
| `change_request_get` | read | One change request by `sys_id` or number (`CHG0030001`) |
| `change_request_create` | write | New change request; your instance's own approval process then applies |
| `user_lookup`, `group_lookup` | read | By `sys_id`, e-mail, user name or part of a name; only name, e-mail, title, department, manager and activity are returned |
| `cmdb_ci_lookup` | read | Configuration items by name, `sys_id`, IP address or serial number; `class` may be `cmdb_ci` or a subclass such as `cmdb_ci_server` and nothing else |
| `knowledge_search` | read | Published articles the integration user may read: title, number, snippet |

Every **write waits for a person's signed approval** until a Head Chef switches that off for the tool. Tools are named `<install>_<tool>`, for example `servicenow_incident_create`.

What a model cannot do through this pack: pick another table (each tool has its own; only the CMDB class is chosen, and only inside `cmdb_ci`), add a condition to a query (a filter value containing `^` is refused, so `^OR` cannot be smuggled in), write a column the schema does not name (custom columns go in `custom_fields`, and only `u_` columns are accepted), post a customer-visible comment.

## Authentication

| `auth_type` | Secrets (names of vault credentials) |
|---|---|
| `basic` (default) | `username`, `password` of an integration user |
| `oauth2_client_credentials` | `client_id`, `client_secret` of an OAuth application registry entry; the pack exchanges them at `/oauth_token.do` **on the same pinned host** (form fields, as ServiceNow documents), caches the token in memory and fetches a new one once when the instance answers 401 |
| `bearer` | `token`, a bearer token you obtained yourself (it expires; rebind when it does) |

Create the credentials with `allowed_hosts` set to the instance host (`acme.service-now.com`): a pack uses only pinned credentials and refuses to install, `secret_not_pinned`, otherwise. The integration user should hold the narrowest roles that do the job (for example `itil` for incidents and changes, read access to the other tables); the pack cannot widen what ServiceNow's ACLs refuse and passes the refusal on as `forbidden`.

## Install

```bash
agentoven secret create sn-user --allow-host acme.service-now.com
agentoven secret create sn-pass --allow-host acme.service-now.com
agentoven packs egress allow acme.service-now.com      # only if your network resolves it privately
agentoven packs install servicenow --name sn --set instance=acme \
    --secret username=vault://sn-user --secret password=vault://sn-pass
agentoven packs check sn                                # reads one sys_user row
```

The dashboard's **Tool packs** page offers the same: choose ServiceNow in the catalog, type the instance, pick the authentication and the secrets.

## Limits and errors

- Calls per minute, per install: 120 reads and 20 writes by default (settings `rate_limit_read_per_minute`, `rate_limit_write_per_minute`, bounded at 600 and 120). A call over budget returns `rate_limited` with `retry_after_seconds` and never reaches ServiceNow. The budget is kept in memory per server process, so N replicas admit N times the rate; ServiceNow's own limits are the backstop.
- Lists page by `offset` and `limit` (at most 100); the answer carries `total` (from `X-Total-Count`) and `next_offset`.
- A failure comes back as `{"error": {"kind", "http_status", "message", "retryable", "hint", "retry_after_seconds"}}` with `kind` one of `auth_failed`, `forbidden`, `not_found`, `invalid_request`, `rate_limited`, `vendor_unavailable`. ServiceNow hides rows an ACL denies as an empty list or a 404, so "not found" can also mean "not allowed".
- Reference fields come back as their `sys_id`; pass `display_values: true` to get names instead. Empty columns are left out.

## What a real instance has to confirm

1. The Knowledge Management API path and answer (`/api/sn_km_api/knowledge/articles`, `result.meta`, `result.articles[]`) on your release, and that the integration user may use it.
2. That a `PATCH` of `work_notes` through the Table API journals a work note with the integration user as author.
3. `type` on `change_request` (normal, standard, emergency) and the date format of `start_date`/`end_date` (`YYYY-MM-DD HH:MM:SS`, UTC) on your instance; whether your instance needs the Change Management API (`/api/sn_chg_rest/change`) instead of the Table API for create.
4. The OAuth client credentials grant: that your release supports it (it was added in recent releases), the token endpoint path `/oauth_token.do`, and that form-field client authentication is accepted.
5. The encoded-query date comparison (`opened_at>=2026-01-01 00:00:00`) and `sysparm_exclude_reference_link`.
6. The integration user's roles: what the tools need and nothing more.
