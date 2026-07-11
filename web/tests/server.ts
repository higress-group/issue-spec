import { http, HttpResponse } from "msw";
import { setupServer } from "msw/node";

export const fixtureContext = {
  user: { id: "aaaaaaaa-aaaa-4aaa-8aaa-aaaaaaaaaaaa", login: "alice", display_name: "Alice", email: "alice@example.test", site_admin: true },
  credential: { kind: "session", scope_mode: "identity", repository_restricted: false, absolute_expires_at: "2030-01-01T00:00:00Z", idle_expires_at: "2029-12-31T12:00:00Z" },
  session: { csrf_cookie_name: "issue_spec_csrf", csrf_header_name: "X-CSRF-Token" },
  allowed_actions: ["site.admin"],
  organizations: [{ id: "bbbbbbbb-bbbb-4bbb-8bbb-bbbbbbbbbbbb", name: "acme", display_name: "Acme", effective_permission: "admin", container_only: false, allowed_actions: ["organization.read", "organization.admin", "credential.admin"] }],
};

export const fixtureMeta = {
  api_version: "v1",
  features: { bootstrap: true, personal_access_tokens: true, organizations: true, source_bindings: false, webhooks: false, change_boards: false, runner: false, recovery_exchange: true },
};

export const handlers = [
  http.get("http://localhost/api/v1/meta", () => HttpResponse.json(fixtureMeta)),
  http.get("http://localhost/api/v1/context", () => HttpResponse.json(fixtureContext)),
  http.get("http://localhost/api/v1/auth/providers", () => HttpResponse.json({ providers: [{ name: "company-oidc", kind: "oidc" }] })),
  http.get("http://localhost/api/v1/bootstrap", () => HttpResponse.json({ available: true, completed: false, representation_version: 1 })),
  http.get("http://localhost/api/v1/pats", () => HttpResponse.json({ tokens: [] })),
];

export const server = setupServer(...handlers);
