# GitHub OAuth quickstart (v1)

This quickstart covers GitHub.com. For GitHub Enterprise, start from
[github-enterprise.json](examples/github-enterprise.json) and use your
operator-controlled API endpoints.

1. Choose employee-visible root origins. Prefer HTTPS. If the deployment is
   intentionally HTTP-only, complete the
   [trusted internal HTTP](trusted-internal-http.md) checklist first.
2. Generate a stable provider UUID (`uuidgen | tr '[:upper:]' '[:lower:]'`).
   The UUID is not a secret; do not regenerate it during client-secret rotation.
3. Create a GitHub OAuth App. Set its homepage to `WEB_PUBLIC_URL` and its
   authorization callback to exactly
   `{API_PUBLIC_URL}/api/v1/auth/github/callback`.
4. Choose an explicit admission policy. Use
   [github-unrestricted.json](examples/github-unrestricted.json), or follow
   [organization admission](github-organization-admission.md).
5. Create the provider file using the `0600` process in
   [provider-file.md](provider-file.md). Replace all placeholders, including
   the OAuth client ID and secret.
6. Set `ENVIRONMENT=production`, `API_PUBLIC_URL`, `WEB_PUBLIC_URL`,
   `TRANSPORT_POSTURE`, and the absolute `AUTH_PROVIDERS_FILE` path along with
   the server's core secret-file variables. Restart the server.
7. Confirm `GET /api/v1/auth/providers` lists `github`; it must not expose the
   client secret. Sign in through the SPA, then open `/user` in the same
   browser and confirm the intended external identity and avatar.
8. Assign local authorization explicitly. A successful GitHub login or
   organization membership does not grant repository, role, or runner access.

The callback is browser traffic: GitHub redirects the employee's browser to
the server. GitHub does not need inbound network access to the callback. The
server does require outbound access to GitHub OAuth/API endpoints and approved
avatar origins.

GitHub.com defaults are:

- authorization/token endpoints from the standard GitHub OAuth endpoint;
- user API `https://api.github.com/user`;
- avatar origin `https://avatars.githubusercontent.com`;
- unrestricted default scopes `read:user` and `user:email`.

Do not paste callback URLs containing `code` or `state` into tickets or logs.
