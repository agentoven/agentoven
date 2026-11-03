# Tool packs: governed tools for the systems your agents work with (Pro)

A tool pack is a named, versioned set of tools for one kind of system. You install it into a kitchen by choosing **secrets**, not by writing a tool server. The control plane makes the calls itself, so the rules that matter are enforced in one place:

- anything that **changes** an outside system waits for a person's approval, until a Head Chef switches that off for that tool;
- credentials are `vault://` **references** to secrets you already keep, **pinned to the hosts the pack sends them to** (`allowed_hosts` on the secret: an install is refused, `secret_not_pinned`, until it is, and a pin that moves later stops the calls); a pack never holds a value, and values are removed from what a tool returns;
- a pack reaches **public addresses only**; your own network has to be allowed, by name, per kitchen;
- with skill approval required (`AGENTOVEN_SKILL_APPROVAL=required`), an install **waits for people** who did not install it, and a change to it goes back for review;
- every install, change and write call is in the audit trail.

## What is in the catalog

| Pack | Tools | Secrets |
|---|---|---|
| `postgres-readonly` | `query`, `list_tables`, `describe_table` (all read) | `dsn`: `postgres://user:password@host:5432/db?sslmode=require` of a role that can only `SELECT` |
| `notifications` | `send_email`, `post_slack_message` (both write) | `smtp`: `smtp://user:pass@host:587?from=alerts@example.com&starttls=required`; `slack_webhook`: the incoming-webhook URL. Bind one or both; only the tools whose secret is bound are installed |

`postgres-readonly` reads each statement with PostgreSQL's own parser and runs only one `SELECT`, `VALUES`, `WITH ... SELECT` or `EXPLAIN` (without `ANALYZE`), with no `INTO` and no row locks, calling only built-in functions on an allowlist (aggregates, arithmetic, text, dates, JSON, arrays, a few catalog readers; nothing that reads files, reaches other servers, takes locks or sleeps), in a read-only transaction, with values bound as parameters (`$1`, `$2`, never inside the SQL), a statement timeout and a row cap. A refusal says what to change. **The connection string must name a role that is not a superuser and not a member of `pg_read_server_files`, `pg_write_server_files` or `pg_execute_server_program`:** the pack refuses to connect as one. Beyond that, the role should have `SELECT` on what the agent may see and nothing else.

Any REST API with an OpenAPI 3 document can be imported as a pack of its own.

## Install from the command line

```bash
agentoven secret create wh-dsn          # value from stdin or a prompt, never a flag
agentoven packs list --catalog
agentoven packs install postgres-readonly --name warehouse --secret dsn=vault://wh-dsn
agentoven packs list                    # status, tools, which wait for approval
agentoven packs check warehouse         # the database answers
```

The tools are named `<install>_<tool>` (`warehouse_query`). Give them to an agent as ordinary tool ingredients, or attach the skill `pack-warehouse` to give it all of them at once.

A database or mail server on your own network resolves to a private address, so a Head Chef allows it first:

```bash
agentoven packs egress allow db.internal        # or *.corp.example
```

Cloud metadata addresses can never be allowed.

## Import an API

```bash
agentoven tool import-openapi pets --spec ./petstore.yaml --preview     # the operations, read or write
agentoven tool import-openapi pets --spec https://api.example.com/openapi.json \
    --operation listPets --operation getPet --operation createPet \
    --auth header --auth-name X-Api-Key --secret value=vault://pets-key
```

- `--spec` is a file or a URL (the server fetches a URL, subject to the allowlist above).
- With no `--operation`, every **read** (GET) operation is imported. A write is imported only when you name it, and then waits for approval. The classification is by HTTP method; a POST that only searches is still a write.
- The API's address is **pinned** at import (`--base-url`, default the document's first server). A call can never go to another host.
- `--auth` is `none`, `bearer` (role `token`), `header` or `query` (role `value`), `basic` (`username`, `password`) or `oauth2_client_credentials` (`client_id`, `client_secret`, `--token-url`, `--scopes`). Without it the document's own security scheme is used.
- Responses are cut at 1 MiB, calls at 30 s, and at most 3 redirects are followed, only within the same host.
- `--replace` imports a new document over an existing import of the same name (an upgrade).

## Approval, switches and who may do what

| | Permission | Roles |
|---|---|---|
| Read the catalog, installs and allowlist | `toolpack:read` | Chef, Baker, Taster, Auditor, Head Chef |
| Install, import, upgrade, rebind secrets, check | `toolpack:install` | Chef, Head Chef |
| Uninstall | `toolpack:remove` | Chef, Head Chef |
| Switch a tool or its approval on or off; edit the allowlist; install with an approval off | `toolpack:govern` | Head Chef |

```bash
agentoven packs tool notifications send_email --no-approval   # Head Chef only; audited
agentoven packs tool notifications send_email --disable
```

An approval is a signed decision like any tool approval: the run pauses, a person approves in the approvals queue (or `agentoven task` / the API), and the tool runs once. A gated tool cannot be called through the MCP endpoint (`POST /mcp`) at all, because it has no way to ask; run it through an agent.

## From the SDKs

```python
client.install_tool_pack("postgres-readonly", {"dsn": "vault://wh-dsn"}, name="warehouse")
client.import_openapi("pets", spec=open("petstore.yaml").read(), operations=["getPet"],
                      secrets={"value": "vault://pets-key"})
client.list_tool_packs()
```

```ts
await pro.installToolPack({ pack: 'postgres-readonly', secrets: { dsn: 'vault://wh-dsn' }, name: 'warehouse' });
await pro.importOpenApi({ name: 'pets', spec, operations: ['getPet'], secrets: { value: 'vault://pets-key' } });
```

## Limits worth knowing

- Pin your secrets to the hosts they belong to (`agentoven secret update --allow-host`): a pack uses only pinned credentials. Whoever may change a secret's pin can send it to the hosts they choose, so the right to do that is as sensitive as the secret.
- A pack's tools cannot be edited or removed through the generic tools API (`PUT`/`DELETE /tools/{name}` answer `403 tool_managed`); use the pack's own commands.
- PostgreSQL only for SQL; email is plain text; Slack is text only; no multipart or form bodies in imported APIs.
- Design and the full list of what is not built: Pro ADR-0048 and OSS ADR-0031.
