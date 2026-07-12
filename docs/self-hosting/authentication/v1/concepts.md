# Authentication concepts (v1)

Authentication answers who the user is. Admission decides whether an external
identity may create or resume a local account. Authorization remains local:
signing in never implicitly grants an issue-spec role, repository permission,
or runner capability.

## Identity flow

1. The SPA reads enabled providers from `GET /api/v1/auth/providers`.
2. The browser starts a login transaction. The server uses state, PKCE, and a
   short-lived transaction cookie; OIDC also uses nonce.
3. The provider redirects the employee browser to
   `{API_PUBLIC_URL}/api/v1/auth/{provider-name}/callback`.
4. The server verifies the callback, normalizes the external identity, applies
   provider admission, and links it to a local user.
5. The server creates its same-origin session and redirects to the SPA.

Provider subjects are the durable identity key. Login, display name, email,
and avatar are profile attributes and may change. A provider UUID must remain
stable across restarts and secret rotation; changing it creates a different
identity namespace.

## Boundaries

- `AUTH_PROVIDERS_FILE` is configuration and contains secrets. It is not a user
  database or an authorization policy store.
- GitHub organization admission is evaluated before local account access. Its
  evidence is recorded independently from local roles.
- OIDC group claims are profile/provider data only in v1. They do not grant
  local roles.
- The avatar endpoint is a same-origin, allowlisted proxy. The browser never
  needs direct access to a provider avatar URL.
- `API_PUBLIC_URL` and `WEB_PUBLIC_URL` are operator declarations, not values
  inferred from request headers.

Use HTTPS for production whenever possible. `trusted-internal-http` is an
explicit exception for controlled employee networks, not a development mode.
