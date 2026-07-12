# GitHub organization admission (v1)

Set `admission.mode` to `organization-restricted` when only active members of
named GitHub organizations may sign in. Configure 1–16 unique organizations.
Use the stable numeric organization `id` when known; the server verifies it
against the observed login to detect renamed or reused organization identity.

GitHub.com uses `https://api.github.com/user/memberships/orgs` by default.
Restricted mode adds `read:org`. GitHub Enterprise production configurations
must set `membership_url`; it must be HTTPS and have the same origin as
`user_url`.

An active membership in any configured organization is allowed. Pending
invitations and non-members are denied. Organization SSO/app authorization or
scope problems are indeterminate failures, not evidence that the user is a
non-member. The browser intentionally shows a generic authentication failure;
operators correlate the request ID with these grep-friendly server classes:

- `github_admission_no_active_membership`
- `github_admission_pending`
- `github_admission_organization_identity_mismatch`
- `github_admission_missing_scope`
- `github_admission_sso_restricted`
- `github_admission_rate_limited`
- `github_admission_upstream_unavailable`
- `github_admission_invalid_response`

Before rollout, test an active member, a pending invitation, a non-member, an
account requiring SSO authorization, and provider/API failure. Admission does
not replace local authorization; admitted users still need explicit local
roles.

Use [github-organization.json](examples/github-organization.json) as the
placeholder-only starting point.
