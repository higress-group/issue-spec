# Authentication provider file (v1)

Set `AUTH_PROVIDERS_FILE` to an absolute path. The target must be a non-empty,
regular, non-symlink file no larger than 64 KiB. Group or other permission bits
are rejected; use mode `0600`. JSON decoding is strict: unknown fields,
multiple JSON values, an empty `providers` array, duplicate names, and
duplicate UUIDs are errors.

Create the file without exposing a secret through shell history or process
arguments:

```sh
install -m 600 /dev/null /run/issue-spec-secrets/auth-providers.json
${EDITOR:?set EDITOR} /run/issue-spec-secrets/auth-providers.json
```

Prefer a secret manager that mounts the final file with the same ownership and
permissions. Never print the file, callback query, session cookie, or provider
secret in diagnostics.

## Top-level schema

```json
{ "providers": [/* one or more provider objects */] }
```

## Provider fields

| Field | Required | Contract |
| --- | --- | --- |
| `id` | yes | Stable, non-zero UUID. Keep it unchanged during rotation. |
| `name` | yes | Unique callback name, 1–64 lowercase ASCII letters, digits, or `-`. |
| `kind` | yes | `github-oauth` or `oidc`. |
| `issuer` | yes | Provider issuer; trailing `/` is normalized away. |
| `client_id` | yes | OAuth/OIDC client identifier. |
| `client_secret` | yes | Secret value; keep only in the protected provider file. |
| `scopes` | no | Provider scopes. Defaults are provider-specific. |
| `auth_url` | no | GitHub Enterprise authorization URL; set with `token_url`. |
| `token_url` | no | GitHub Enterprise token URL; set with `auth_url`. |
| `user_url` | no | GitHub user API URL; defaults to `https://api.github.com/user`. |
| `avatar_origins` | no | Canonical HTTPS origins allowed for provider avatars. |
| `admission` | GitHub in production | Explicit GitHub admission policy; forbidden for OIDC. |

The callback URL is always
`{API_PUBLIC_URL}/api/v1/auth/{name}/callback`. Register that exact value with
the provider. It is not derived from `Host` or forwarding headers.

## GitHub admission fields

| Field | Required | Contract |
| --- | --- | --- |
| `mode` | yes | `unrestricted` or `organization-restricted`. |
| `organizations` | restricted only | 1–16 objects with unique normalized logins. |
| `membership_url` | GitHub Enterprise restricted | HTTPS URL on the same origin as `user_url`. |

Each organization has required `login` and optional `id`. The ID is a
canonical positive decimal string and, when known, protects against an
organization login being reused. `unrestricted` must not set organizations or
`membership_url`. Restricted mode adds and de-duplicates the `read:org` scope.

Canonical, placeholder-only examples live in [examples](examples/). Replace
every `__ISSUE_SPEC_...__` value through your secret-management workflow before
starting the server.
