import axe from "axe-core";
import { http, HttpResponse } from "msw";
import { screen, waitFor } from "@testing-library/react";
import userEvent from "@testing-library/user-event";
import { Route, Routes } from "react-router-dom";
import { describe, expect, it } from "vitest";
import { renderApp } from "../../tests/render";
import { server } from "../../tests/server";
import { IntegrationsPage } from "./integrations-page";

const orgId = "bbbbbbbb-bbbb-4bbb-8bbb-bbbbbbbbbbbb";
const repoId = "cccccccc-cccc-4ccc-8ccc-cccccccccccc";
const webhookId = "dddddddd-dddd-4ddd-8ddd-dddddddddddd";
const deliveryId = "eeeeeeee-eeee-4eee-8eee-eeeeeeeeeeee";
const eventId = "ffffffff-ffff-4fff-8fff-ffffffffffff";

describe("repository integrations workspace", () => {
  it("publishes and deactivates a real source binding contract", async () => {
    let published: unknown;
    let deactivated = false;
    server.use(
      metaHandler(),
      http.get(sourcePath("/active"), () => HttpResponse.json(bindingFixture())),
      http.post(sourcePath(""), async ({ request }) => { published = await request.json(); return HttpResponse.json(bindingFixture({ version: 2 }), { status: 201 }); }),
      http.delete(sourcePath("/active"), () => { deactivated = true; return new HttpResponse(null, { status: 204 }); }),
    );
    const { container } = renderIntegration("source");
    expect(await screen.findByRole("heading", { name: "Source connection" })).toBeVisible();
    expect((await screen.findAllByText("higress-group/issue-spec"))[0]).toBeVisible();
    await userEvent.setup().click(screen.getByRole("button", { name: "Publish new version" }));
    await waitFor(() => expect(published).toEqual({ provider_key: "github", external_repository_id: "higress-group/issue-spec", clone_url: "https://github.com/higress-group/issue-spec.git", web_url: "https://github.com/higress-group/issue-spec", default_branch: "main" }));
    const deactivate = screen.getByRole("button", { name: "Deactivate" });
    await userEvent.setup().click(deactivate);
    await userEvent.setup().click(screen.getByRole("button", { name: "Confirm deactivation" }));
    await waitFor(() => expect(deactivated).toBe(true));
    expect((await axe.run(container)).violations).toEqual([]);
  });

  it("creates a repository-scoped webhook and exposes its secret once", async () => {
    let created: unknown;
    server.use(
      metaHandler(),
      http.get(webhookCollectionPath(), () => HttpResponse.json({ subscriptions: [] })),
      http.get(deliveryCollectionPath(), () => HttpResponse.json({ deliveries: [] })),
      http.post(webhookCollectionPath(), async ({ request }) => { created = await request.json(); return HttpResponse.json(webhookFixture({ secret: "show-once-secret", secret_version: 1 }), { status: 201 }); }),
    );
    renderIntegration("webhooks");
    await userEvent.setup().click(await screen.findByRole("button", { name: "New webhook" }));
    await userEvent.setup().type(screen.getByRole("textbox", { name: /^Receiver URL/ }), "http://127.0.0.1:19090/api/v1/runner/webhooks");
    await userEvent.setup().click(screen.getByRole("button", { name: "Create route" }));
    await waitFor(() => expect(created).toMatchObject({ repository_id: repoId, url: "http://127.0.0.1:19090/api/v1/runner/webhooks", event_types: ["issue_comment.created", "issue_comment.edited"], retry: { max_attempts: 8, initial_backoff: "1s", max_backoff: "5m" } }));
    expect(await screen.findByRole("dialog", { name: "Webhook secret v1" })).toHaveTextContent("show-once-secret");
  });

  it("pauses and rotates a route, inspects attempts, and replays the immutable delivery", async () => {
    let update: unknown;
    let replayed = false;
    server.use(
      metaHandler(),
      http.get(webhookCollectionPath(), () => HttpResponse.json({ subscriptions: [webhookFixture()] })),
      http.patch(`${webhookCollectionPath()}/${webhookId}`, async ({ request }) => { update = await request.json(); return HttpResponse.json(webhookFixture({ active: false, representation_version: 4 })); }),
      http.post(`${webhookCollectionPath()}/${webhookId}/rotate-secret`, () => HttpResponse.json(webhookFixture({ secret: "rotated-secret", secret_version: 2 }), { status: 201 })),
      http.get(deliveryCollectionPath(), () => HttpResponse.json({ deliveries: [deliveryFixture()] })),
      http.get(`${deliveryCollectionPath()}/${deliveryId}`, () => HttpResponse.json({ delivery: deliveryFixture(), attempts: [{ id: "12345678-1234-4234-8234-123456789abc", attempt_number: 1, response_status: 503, response_headers: { "Retry-After": ["2"] }, started_at: "2026-07-11T10:00:00Z", completed_at: "2026-07-11T10:00:01Z" }] })),
      http.post(`${deliveryCollectionPath()}/${deliveryId}/redeliver`, () => { replayed = true; return HttpResponse.json(deliveryFixture({ state: "pending" }), { status: 202 }); }),
    );
    renderIntegration("webhooks");
    await userEvent.setup().click(await screen.findByRole("button", { name: "Pause" }));
    await waitFor(() => expect(update).toMatchObject({ active: false, expected_version: 3 }));
    await userEvent.setup().click(screen.getByRole("button", { name: "Rotate secret" }));
    expect(await screen.findByRole("dialog", { name: "Webhook secret v2" })).toHaveTextContent("rotated-secret");
    await userEvent.setup().click(screen.getByRole("button", { name: "I saved it" }));
    await userEvent.setup().click(screen.getByRole("button", { name: /issue_comment.created/ }));
    expect((await screen.findAllByText("HTTP 503"))[0]).toBeVisible();
    await userEvent.setup().click(screen.getByRole("button", { name: "Redeliver event" }));
    await waitFor(() => expect(replayed).toBe(true));
  });
});

function renderIntegration(kind: "source" | "webhooks") {
  const route = `/orgs/${orgId}/repos/${repoId}/integrations/${kind === "source" ? "source" : "webhooks"}`;
  return renderApp(<Routes><Route path="/orgs/:orgId/repos/:repoId/integrations/source" element={<IntegrationsPage kind="source" />} /><Route path="/orgs/:orgId/repos/:repoId/integrations/webhooks" element={<IntegrationsPage kind="webhooks" />} /></Routes>, route);
}
function metaHandler() { return http.get("http://localhost/api/v1/meta", () => HttpResponse.json({ api_version: "v1", features: { bootstrap: true, personal_access_tokens: true, organizations: true, source_bindings: true, webhooks: true, change_boards: true, runner: true, recovery_exchange: true } })); }
function sourcePath(suffix: string) { return `http://localhost/api/v1/orgs/${orgId}/repos/${repoId}/bindings${suffix}`; }
function webhookCollectionPath() { return `http://localhost/api/v1/orgs/${orgId}/webhooks`; }
function deliveryCollectionPath() { return `http://localhost/api/v1/orgs/${orgId}/repos/${repoId}/deliveries`; }
function bindingFixture(overrides: Record<string, unknown> = {}) { return { id: "11111111-1111-4111-8111-111111111111", provider_key: "github", external_repository_id: "higress-group/issue-spec", clone_url: "https://github.com/higress-group/issue-spec.git", web_url: "https://github.com/higress-group/issue-spec", default_branch: "main", version: 1, active: true, created_at: "2026-07-11T09:00:00Z", updated_at: "2026-07-11T09:00:00Z", ...overrides }; }
function webhookFixture(overrides: Record<string, unknown> = {}) { return { id: webhookId, organization_id: orgId, repository_id: repoId, scope_type: "repository", url: "http://127.0.0.1:19090/api/v1/runner/webhooks", active: true, event_types: ["issue_comment.created", "issue_comment.edited"], retry: { max_attempts: 8, initial_backoff: "1s", max_backoff: "5m0s" }, representation_version: 3, created_at: "2026-07-11T09:00:00Z", updated_at: "2026-07-11T09:30:00Z", ...overrides }; }
function deliveryFixture(overrides: Record<string, unknown> = {}) { return { id: deliveryId, scope: { OrgID: orgId, RepoID: repoId }, event_id: eventId, subscription_id: webhookId, state: "dead", next_attempt_at: "2026-07-11T10:05:00Z", last_error: "HTTP 503", representation_version: 2, created_at: "2026-07-11T10:00:00Z", updated_at: "2026-07-11T10:01:00Z", event_type: "issue_comment.created", repository_sequence: 14, secret_version: 1, ...overrides }; }
