# OIDC (v1)

Use [oidc.json](examples/oidc.json) and register the exact callback
`{API_PUBLIC_URL}/api/v1/auth/{name}/callback` with the identity provider. The
server discovers the authorization, token, and JWKS endpoints from `issuer`.
The browser flow uses state, PKCE, and nonce.

The effective scopes include `openid`, `profile`, and `email`, plus configured
values. The server normalizes `preferred_username`, `name`, `email`, and
`picture` when available. Keep the provider UUID stable so the OIDC subject
continues to resolve to the same local identity.

OIDC v1 has no generic group-admission policy. Requesting a group scope or
receiving a group claim does not grant a local role. Enforce provider-side
application assignment if required, and assign issue-spec authorization
separately.

The issuer and discovery endpoints must be reachable by the server. If the
provider emits an avatar `picture`, add its canonical HTTPS origin to
`avatar_origins`; otherwise the same-origin avatar endpoint falls back cleanly.
