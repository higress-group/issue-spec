# Authentication troubleshooting (v1)

Start with the browser-visible request ID and server logs. The SPA deliberately
does not expose provider details or secrets.

| Symptom or log fragment | Check |
| --- | --- |
| `authentication providers: invalid JSON` | Strict JSON syntax and unknown fields. |
| `multiple JSON values are forbidden` | The file must contain one JSON document. |
| `id, safe name, issuer, client id and secret are required` | Required provider fields and lowercase callback name. |
| `production admission policy must be explicit` | Add GitHub `admission.mode`; production has no implicit policy. |
| `github auth_url and token_url must be configured together` | Configure both Enterprise endpoints or neither. |
| `avatar origins must be canonical HTTPS origins` | Remove paths and use a canonical HTTPS origin. |
| provider reports callback mismatch | Compare its registered URL byte-for-byte with `{API_PUBLIC_URL}/api/v1/auth/{name}/callback`. |
| browser cannot return to a private callback | The employee browser, not GitHub, must resolve and reach the declared origin. |
| OIDC discovery/JWKS failure | Verify issuer correctness, CA trust, DNS, firewall, and server egress. |
| avatar shows initials | Check `avatar_origins`, public DNS/address policy, media type, 2 MiB limit, and provider response. |

For restricted GitHub admission, search the request for the exact
`github_admission_*` classes listed in
[organization admission](github-organization-admission.md). A generic browser
error may represent non-membership, pending membership, missing `read:org`, SSO
authorization, rate limiting, or an upstream outage; do not weaken admission
until the class is known.

Safe diagnostics include redacted configuration, the provider name, request
ID, HTTP status class, and `/api/v1/meta` transport fields. Do not print the
provider file, provider secret, Authorization header, cookie values, or a
callback URL containing `code`, `state`, or `error_description`.
