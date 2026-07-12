# Self-hosted authentication

The versioned authentication guide is the operator contract for external
identity providers, admission, and browser sessions. Start with the
[v1 concepts](v1/concepts.md), then choose a provider:

- [GitHub OAuth quickstart](v1/github-oauth.md)
- [OIDC](v1/oidc.md)
- [Provider-file reference](v1/provider-file.md)

Deployments using an employee-only HTTP origin must also follow the
[trusted internal HTTP](v1/trusted-internal-http.md) guide. GitHub deployments
that restrict sign-in to named organizations must follow the
[organization admission](v1/github-organization-admission.md) guide.

Operational references:

- [Avatar proxying](v1/avatars.md)
- [Secret and provider rotation](v1/rotation.md)
- [Troubleshooting](v1/troubleshooting.md)
