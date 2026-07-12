# Avatar proxying (v1)

External profile images are exposed to the SPA through the same-origin route
`/api/v1/avatars/{login}`. The server stores only the normalized provider URL,
then fetches it through a provider-specific `avatar_origins` allowlist.

Origins must be canonical HTTPS origins. Redirects are not followed. Private,
loopback, link-local, and metadata addresses are rejected after DNS resolution
and again for the connected address. Responses are limited to 2 MiB and must
agree on one of `image/png`, `image/jpeg`, `image/gif`, or `image/webp`. Valid
images are cached in memory for five minutes and returned with a content hash
ETag.

GitHub.com defaults to `https://avatars.githubusercontent.com`. GitHub
Enterprise defaults to its issuer origin. OIDC operators should list the
origin used by the `picture` claim. Avatar failure never blocks login; the SPA
renders initials when the proxy has no acceptable image.
