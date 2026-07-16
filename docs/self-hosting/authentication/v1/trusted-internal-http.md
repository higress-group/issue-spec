# Trusted internal HTTP (v1)

Use `TRANSPORT_POSTURE=trusted-internal-http` only when the employee browser
and server are inside a controlled network where TLS termination is genuinely
unavailable. It is accepted in production, including private-IP and internal
DNS origins, but it deliberately reports `secure: false`.

Examples:

```text
API_PUBLIC_URL=http://10.23.8.14:18080
WEB_PUBLIC_URL=http://10.23.8.14:18080
```

```text
API_PUBLIC_URL=http://issues.intra.example:18080
WEB_PUBLIC_URL=http://issues.intra.example:18080
```

Both values must be canonical root origins with the same scheme: no path,
query, fragment, user information, or mixed HTTP/HTTPS. The provider callback
uses the API origin verbatim, for example
`http://issues.intra.example:18080/api/v1/auth/github/callback`.

This posture also permits `http://` webhook receivers for internal Runner
deployments only when every resolved and connect-time receiver address has an
explicit matching `WEBHOOK_ALLOWED_PRIVATE_CIDRS` entry. Despite the legacy
variable name, the CIDR trust boundary may include operator-owned ranges that
are internally routed but are not classified as RFC 1918 private space. An
empty allowlist permits no production HTTP receivers. Configuring a CIDR trusts
plaintext delivery to every otherwise-safe address in that range; loopback,
link-local, unspecified, multicast, and cloud-metadata destinations are always
denied. Receivers outside the explicit allowlist continue to require HTTPS.

## Network checklist

- The employee browser can reach both the identity provider and the declared
  API/web origin through VPN, private routing, or an employee-only network.
- The server can reach provider token, user, membership, discovery, JWKS, and
  approved avatar endpoints as applicable.
- ACLs prevent untrusted clients from observing or modifying plaintext HTTP.
- Operators understand that session and CSRF cookies cannot carry `Secure` on
  HTTP. HttpOnly where applicable, SameSite, bounded TTL, rotation, and CSRF
  protections still apply, but they do not encrypt transport.
- The public origins returned by `GET /api/v1/meta` show
  `transport_posture: "trusted-internal-http"` and `secure: false`. A loopback
  origin may additionally report transport mode `loopback-http`.

Prefer TLS and migrate when possible. Never set development mode to bypass a
production origin check, and never place an HTTP deployment on an untrusted or
shared network.
