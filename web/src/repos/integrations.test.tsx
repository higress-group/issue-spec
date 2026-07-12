import axe from "axe-core";
import { http, HttpResponse } from "msw";
import { screen, waitFor } from "@testing-library/react";
import userEvent from "@testing-library/user-event";
import { Route, Routes } from "react-router-dom";
import { describe, expect, it } from "vitest";
import { renderApp } from "../../tests/render";
import { server } from "../../tests/server";
import { IntegrationsPage, shouldClearDestinationQuery } from "./integrations-page";

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
    await waitFor(() => expect(created).toMatchObject({ repository_id: repoId, url: "http://127.0.0.1:19090/api/v1/runner/webhooks", delivery_format: "issue-spec.v1", signing_mode: "bearer", event_types: ["issue_comment.created", "issue_comment.edited"], retry: { max_attempts: 8, initial_backoff: "1s", max_backoff: "5m" } }));
    expect(await screen.findByRole("dialog", { name: "Webhook secret v1" })).toHaveTextContent("show-once-secret");
  });

  it("pauses and rotates a route, inspects attempts, and replays the immutable delivery", async () => {
    let update: unknown;
    let replayed = false;
    const succeeded = [
      "00000000-0000-4000-8000-000000000001",
      "00000000-0000-4000-8000-000000000002",
      "00000000-0000-4000-8000-000000000003",
      "00000000-0000-4000-8000-000000000004",
    ].map((id, index) => deliveryFixture({ id, state: "succeeded", delivered_at: "2026-07-11T10:00:01Z", last_error: null, repository_sequence: index + 1 }));
    server.use(
      metaHandler(),
      http.get(webhookCollectionPath(), () => HttpResponse.json({ subscriptions: [webhookFixture()] })),
      http.patch(`${webhookCollectionPath()}/${webhookId}`, async ({ request }) => { update = await request.json(); return HttpResponse.json(webhookFixture({ active: false, representation_version: 4 })); }),
      http.post(`${webhookCollectionPath()}/${webhookId}/rotate-secret`, () => HttpResponse.json(webhookFixture({ secret: "rotated-secret", secret_version: 2 }), { status: 201 })),
      http.get(deliveryCollectionPath(), () => HttpResponse.json({ deliveries: [...succeeded, deliveryFixture()] })),
      http.get(`${deliveryCollectionPath()}/${deliveryId}`, () => HttpResponse.json({ delivery: deliveryFixture(), attempts: [{ id: "12345678-1234-4234-8234-123456789abc", attempt_number: 1, response_status: 503, response_headers: { "Retry-After": ["2"] }, started_at: "2026-07-11T10:00:00Z", completed_at: "2026-07-11T10:00:01Z" }] })),
      http.post(`${deliveryCollectionPath()}/${deliveryId}/redeliver`, () => { replayed = true; return HttpResponse.json(deliveryFixture({ state: "pending" }), { status: 202 }); }),
    );
    renderIntegration("webhooks");
    expect(await screen.findByLabelText("4 delivered")).toBeVisible();
    expect(screen.getByLabelText("1 dead letter")).toBeVisible();
    await userEvent.setup().click(await screen.findByRole("button", { name: "Pause" }));
    await waitFor(() => expect(update).toMatchObject({ active: false, expected_version: 3 }));
    await userEvent.setup().click(screen.getByRole("button", { name: "Rotate secret" }));
    expect(await screen.findByRole("dialog", { name: "Webhook secret v2" })).toHaveTextContent("rotated-secret");
    await userEvent.setup().click(screen.getByRole("button", { name: "I saved it" }));
    await userEvent.setup().click(screen.getByRole("button", { name: /issue-spec.*created.*dead/ }));
    expect((await screen.findAllByText("HTTP 503"))[0]).toBeVisible();
    await userEvent.setup().click(screen.getByRole("button", { name: "Replay immutable delivery" }));
    await waitFor(() => expect(replayed).toBe(true));
  });

  it("renders revoked routes as audit-only and never offers resurrection actions", async () => {
    server.use(
      metaHandler(),
      http.get(webhookCollectionPath(), () => HttpResponse.json({ subscriptions: [webhookFixture({ active: false, revoked_at: "2026-07-11T11:00:00Z", representation_version: 4 })] })),
      http.get(deliveryCollectionPath(), () => HttpResponse.json({ deliveries: [] })),
    );
    renderIntegration("webhooks");
    expect(await screen.findByText("revoked")).toBeVisible();
    expect(screen.getByText(/secret destroyed · delivery history retained/i)).toBeVisible();
    expect(screen.queryByRole("button", { name: "Resume" })).not.toBeInTheDocument();
    expect(screen.queryByRole("button", { name: "Configure" })).not.toBeInTheDocument();
    expect(screen.queryByRole("button", { name: "Rotate secret" })).not.toBeInTheDocument();
    expect(screen.queryByRole("button", { name: "Revoke" })).not.toBeInTheDocument();
  });

  it("rejects receiver URLs with query credentials before a request is sent", async () => {
    let requests = 0;
    server.use(
      metaHandler(),
      http.get(webhookCollectionPath(), () => HttpResponse.json({ subscriptions: [] })),
      http.get(deliveryCollectionPath(), () => HttpResponse.json({ deliveries: [] })),
      http.post(webhookCollectionPath(), () => { requests += 1; return HttpResponse.json(webhookFixture({ secret: "unexpected", secret_version: 1 }), { status: 201 }); }),
    );
    renderIntegration("webhooks");
    await userEvent.setup().click(await screen.findByRole("button", { name: "New webhook" }));
    await userEvent.setup().type(screen.getByRole("textbox", { name: /^Receiver URL/ }), "https://runner.example.test/hook?access_token=secret");
    await userEvent.setup().click(screen.getByRole("button", { name: "Create route" }));
    expect(await screen.findByText(/without credentials, query, or fragment/i)).toBeVisible();
    expect(requests).toBe(0);
  });

  it("creates a filtered GitHub notification without rehydrating its query credential", async () => {
    let created: Record<string, unknown> | undefined;
    server.use(
      metaHandler(),
      http.get(webhookCollectionPath(), () => HttpResponse.json({ subscriptions: [] })),
      http.get(deliveryCollectionPath(), () => HttpResponse.json({ deliveries: [] })),
      http.post(webhookCollectionPath(), async ({ request }) => { created = await request.json() as Record<string, unknown>; return HttpResponse.json(webhookFixture({ url: "https://robot.example.test/hook", delivery_format: "github.v3", signing_mode: "hmac-sha256", has_destination_query: true, secret: "github-hmac-secret", secret_version: 1 }), { status: 201 }); }),
    );
    const { container } = renderIntegration("webhooks");
    await userEvent.setup().click(await screen.findByRole("button", { name: "New webhook" }));
    await userEvent.setup().click(screen.getByRole("radio", { name: /GitHub-compatible notification/i }));
    await userEvent.setup().type(screen.getByRole("textbox", { name: /^Receiver URL/ }), "https://robot.example.test/hook?access_token=browser-secret");
    await userEvent.setup().click(screen.getByRole("button", { name: "Create route" }));
    await waitFor(() => expect(created).toMatchObject({ delivery_format: "github.v3", signing_mode: "hmac-sha256", url: "https://robot.example.test/hook?access_token=browser-secret", content_policy: { issue_actions: ["opened", "edited", "closed", "reopened"], comment_classes: ["human-untyped"], actor_classes: ["human"] } }));
    expect(await screen.findByRole("dialog", { name: "Webhook secret v1" })).toHaveTextContent("github-hmac-secret");
    expect((await axe.run(container)).violations).toEqual([]);
  });

  it("shows encrypted-query and suppression state without returning credential material", async () => {
    const notification = webhookFixture({ url: "https://robot.example.test/hook", delivery_format: "github.v3", signing_mode: "hmac-sha256", has_destination_query: true });
    server.use(
      metaHandler(),
      http.get(webhookCollectionPath(), () => HttpResponse.json({ subscriptions: [notification] })),
      http.get(deliveryCollectionPath(), () => HttpResponse.json({ deliveries: [] })),
      http.get(`${webhookCollectionPath()}/${webhookId}/suppressions`, () => HttpResponse.json({ suppressions: [{ id: "99999999-9999-4999-8999-999999999999", organization_id: orgId, repository_id: repoId, event_id: eventId, subscription_id: webhookId, event_type: "issue_comment.created", action: "created", issue_kind: "proposal", comment_class: "typed", actor_class: "human", reason: "comment_class_filtered", created_at: "2026-07-11T10:00:00Z" }] })),
    );
    renderIntegration("webhooks");
    expect(await screen.findByText("Encrypted destination credential")).toBeVisible();
    await userEvent.setup().click(screen.getByRole("button", { name: "Configure" }));
    expect(screen.getByRole("textbox", { name: /^Receiver URL/ })).toHaveValue("https://robot.example.test/hook");
    expect(screen.getByText(/encrypted query is intentionally absent/i)).toBeVisible();
    expect(screen.queryByText(/access_token/i)).not.toBeInTheDocument();
    await userEvent.setup().click(screen.getByRole("button", { name: "Suppressions" }));
    expect(await screen.findByText(/comment class filtered/i)).toBeVisible();
  });

  it("clears a redacted destination query when the receiver host or path changes", () => {
	const stored = { url: "https://robot.example.test/hook", has_destination_query: true };
	expect(shouldClearDestinationQuery(stored, {
	  url: "https://other.example.test/other-path", clear_destination_query: false,
	})).toBe(true);
	expect(shouldClearDestinationQuery(stored, {
	  url: "https://robot.example.test/other-path", clear_destination_query: false,
	})).toBe(true);
	expect(shouldClearDestinationQuery(stored, {
	  url: "https://other.example.test/other-path?access_token=replacement", clear_destination_query: false,
	})).toBe(false);
	expect(shouldClearDestinationQuery(stored, {
	  url: stored.url, clear_destination_query: false,
	})).toBe(false);
  });

  it("does not fetch or reveal integration configuration without integrations.manage", async () => {
    let webhookReads = 0;
    server.use(metaHandler(), http.get(webhookCollectionPath(), () => { webhookReads += 1; return HttpResponse.json({ subscriptions: [] }); }));
    renderIntegration("webhooks", ["read"]);
    expect(await screen.findByText("Integration management required")).toBeVisible();
    expect(webhookReads).toBe(0);
  });
});

function renderIntegration(kind: "source" | "webhooks", allowedActions?: string[]) {
  server.use(repositoryHandler(), repositoryAccessHandler(allowedActions));
  const route = `/orgs/${orgId}/repos/${repoId}/integrations/${kind === "source" ? "source" : "webhooks"}`;
  return renderApp(<Routes><Route path="/orgs/:orgId/repos/:repoId/integrations/source" element={<IntegrationsPage kind="source" />} /><Route path="/orgs/:orgId/repos/:repoId/integrations/webhooks" element={<IntegrationsPage kind="webhooks" />} /></Routes>, route);
}
function repositoryHandler() { return http.get(`http://localhost/api/v1/orgs/${orgId}/repos/${repoId}`, () => HttpResponse.json({ id: repoId, organization_id: orgId, name: "issue-spec", display_name: "Issue Spec", description: "Issue-native specifications", visibility: "private", default_branch: "main", contribution_policy: "members", representation_version: 1 })); }
function repositoryAccessHandler(allowed_actions = ["read", "integrations.manage"]) { return http.get(`http://localhost/api/v1/context/orgs/${orgId}/repos`, () => HttpResponse.json({ repositories: [{ repository: { id: repoId, organization_id: orgId, name: "issue-spec", display_name: "Issue Spec", visibility: "private", contribution_policy: "members" }, effective_permission: allowed_actions.includes("integrations.manage") ? "maintain" : "read", allowed_actions }] })); }
function metaHandler() { return http.get("http://localhost/api/v1/meta", () => HttpResponse.json({ api_version: "v1", features: { bootstrap: true, personal_access_tokens: true, organizations: true, source_bindings: true, webhooks: true, change_boards: true, runner: true, recovery_exchange: true } })); }
function sourcePath(suffix: string) { return `http://localhost/api/v1/orgs/${orgId}/repos/${repoId}/bindings${suffix}`; }
function webhookCollectionPath() { return `http://localhost/api/v1/orgs/${orgId}/webhooks`; }
function deliveryCollectionPath() { return `http://localhost/api/v1/orgs/${orgId}/repos/${repoId}/deliveries`; }
function bindingFixture(overrides: Record<string, unknown> = {}) { return { id: "11111111-1111-4111-8111-111111111111", provider_key: "github", external_repository_id: "higress-group/issue-spec", clone_url: "https://github.com/higress-group/issue-spec.git", web_url: "https://github.com/higress-group/issue-spec", default_branch: "main", version: 1, active: true, created_at: "2026-07-11T09:00:00Z", updated_at: "2026-07-11T09:00:00Z", ...overrides }; }
const contentPolicy = { issue_actions: ["opened", "edited", "closed", "reopened"], comment_actions: ["created", "edited"], issue_kinds: ["ordinary", "proposal", "design", "implement"], comment_classes: ["human-untyped"], actor_classes: ["human"] };
function webhookFixture(overrides: Record<string, unknown> = {}) { return { id: webhookId, organization_id: orgId, repository_id: repoId, scope_type: "repository", url: "http://127.0.0.1:19090/api/v1/runner/webhooks", active: true, event_types: ["issue_comment.created", "issue_comment.edited"], delivery_format: "issue-spec.v1", signing_mode: "bearer", content_policy: contentPolicy, has_destination_query: false, retry: { max_attempts: 8, initial_backoff: "1s", max_backoff: "5m0s" }, representation_version: 3, created_at: "2026-07-11T09:00:00Z", updated_at: "2026-07-11T09:30:00Z", ...overrides }; }
function deliveryFixture(overrides: Record<string, unknown> = {}) { return { id: deliveryId, scope: { OrgID: orgId, RepoID: repoId }, event_id: eventId, subscription_id: webhookId, state: "dead", next_attempt_at: "2026-07-11T10:05:00Z", last_error: "HTTP 503", representation_version: 2, created_at: "2026-07-11T10:00:00Z", updated_at: "2026-07-11T10:01:00Z", event_type: "issue_comment.created", delivery_format: "issue-spec.v1", event_name: "issue-spec", action: "created", repository_sequence: 14, secret_version: 1, ...overrides }; }
