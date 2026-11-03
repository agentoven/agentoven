# Secrets in tools and notification channels

## A tool's authentication

A tool's `auth_config` is one of three shapes; any other is refused with a 400:

| type | fields |
|---|---|
| `bearer` | `credential_ref` (or a literal `token`) |
| `api-key` | `header`, `credential_ref` (or a literal `key`) |
| `basic` | `username`, `credential_ref` holding the password (or a literal `password`) |

Prefer `credential_ref`: the secret stays in a kitchen credential and a rotation reaches the tool at once. In Pro a literal secret is moved into the credential vault when the tool is saved (`tool-<name>`), and tools saved earlier are moved by the startup pass `AGENTOVEN_SEAL_PROVIDER_KEYS=true` (`dry-run` lists them) or `POST /models/providers/seal-keys`. A tool saved earlier with a shape that is now refused (a client secret, a header set) has each such field moved into its own credential and keeps only `<field>_ref`; re-register it as one of the three shapes. OAuth client credentials and custom header sets are not supported. Reading a tool shows secrets as `****`; writing the mask back keeps the stored value.

## A webhook channel's signing secret

Send `secret` when you create or update a `webhook` channel. It is never returned: reads say `has_secret`, and in Pro `secret_ref` names the credential (`channel-<name>-secret`) that holds it. Each delivery carries `X-AgentOven-Signature: sha256=<hex HMAC-SHA256 of the body>`. Update without `secret` to keep it, with `"secret": ""` to remove it, with a new value to rotate it. If the credential cannot be read the message is not sent.
