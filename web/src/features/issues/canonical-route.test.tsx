import { QueryClient, QueryClientProvider } from "@tanstack/react-query";
import { http, HttpResponse } from "msw";
import { render, screen } from "@testing-library/react";
import { afterEach, describe, expect, it, vi } from "vitest";
import { createMemoryRouter, RouterProvider, useLocation } from "react-router-dom";
import { server } from "../../../tests/server";
import { api } from "../../lib/api/resources";
import contribution from "./contribution";
import { CanonicalIssueRoutePage } from "./canonical-route";

const orgId = "bbbbbbbb-bbbb-4bbb-8bbb-bbbbbbbbbbbb";
const repoId = "cccccccc-cccc-4ccc-8ccc-cccccccccccc";

afterEach(() => vi.restoreAllMocks());

describe("canonical issue WebURL route", () => {
  it("is registered as an authenticated issue feature route", () => {
    expect(contribution.routes?.some((route) => route.path === ":owner/:repo/issues/:issueNumber")).toBe(true);
  });

  it("resolves visible names case-insensitively and replaces into the UUID route with search and hash intact", async () => {
    server.use(
      http.get("http://localhost/api/v1/context", () => HttpResponse.json(contextFixture())),
      http.get(`http://localhost/api/v1/context/orgs/${orgId}/repos`, () => HttpResponse.json(repositoriesFixture())),
    );
    const { router } = renderCanonical("/BROWSER-E2E/ISSUE-SPEC-E2E/issues/2?view=timeline#issuecomment-1987124517582305");
    expect(await screen.findByTestId("resolved-location")).toHaveTextContent(`/issues/${orgId}/${repoId}/2?view=timeline#issuecomment-1987124517582305`);
    expect(router.state.historyAction).toBe("REPLACE");
  });

  it("shows a stable loading state while authenticated context is unresolved", () => {
    vi.spyOn(api, "context").mockReturnValue(new Promise(() => undefined));
    renderCanonical("/browser-e2e/issue-spec-e2e/issues/2");
    expect(screen.getByRole("status")).toHaveTextContent("Resolving canonical issue URL");
  });

  it("conceals an unknown organization without requesting a repository collection", async () => {
    let repositoriesRequested = false;
    server.use(
      http.get("http://localhost/api/v1/context", () => HttpResponse.json(contextFixture({ organizations: [] }))),
      http.get("http://localhost/api/v1/context/orgs/:orgId/repos", () => { repositoriesRequested = true; return HttpResponse.json(repositoriesFixture()); }),
    );
    renderCanonical("/hidden/issue-spec-e2e/issues/2");
    expect(await screen.findByRole("heading", { name: "That issue desk is not here" })).toBeVisible();
    expect(repositoriesRequested).toBe(false);
  });

  it("conceals a repository absent from the permission-filtered context response", async () => {
    server.use(
      http.get("http://localhost/api/v1/context", () => HttpResponse.json(contextFixture())),
      http.get(`http://localhost/api/v1/context/orgs/${orgId}/repos`, () => HttpResponse.json({ repositories: [] })),
    );
    renderCanonical("/browser-e2e/hidden/issues/2#issuecomment-1");
    expect(await screen.findByRole("heading", { name: "That issue desk is not here" })).toBeVisible();
  });

  it("rejects non-canonical or unsafe issue numbers", async () => {
    server.use(http.get("http://localhost/api/v1/context", () => HttpResponse.json(contextFixture())));
    renderCanonical("/browser-e2e/issue-spec-e2e/issues/9007199254740992");
    expect(await screen.findByRole("heading", { name: "That issue desk is not here" })).toBeVisible();
  });
});

function renderCanonical(initialEntry: string) {
  const client = new QueryClient({ defaultOptions: { queries: { retry: false } } });
  const router = createMemoryRouter([
    { path: "/:owner/:repo/issues/:issueNumber", element: <CanonicalIssueRoutePage /> },
    { path: "/issues/:orgId/:repoId/:issueNumber", element: <LocationProbe /> },
    { path: "*", element: <div>Unexpected route</div> },
  ], { initialEntries: [initialEntry] });
  return { router, ...render(<QueryClientProvider client={client}><RouterProvider router={router} /></QueryClientProvider>) };
}

function LocationProbe() {
  const location = useLocation();
  return <output data-testid="resolved-location">{location.pathname}{location.search}{location.hash}</output>;
}

function contextFixture(overrides: Record<string, unknown> = {}) {
  return { user: { id: "aaaaaaaa-aaaa-4aaa-8aaa-aaaaaaaaaaaa", login: "alice", display_name: "Alice", site_admin: false }, credential: { kind: "session", scope_mode: "identity", repository_restricted: false }, allowed_actions: [], organizations: [{ id: orgId, name: "browser-e2e", display_name: "Browser E2E", effective_permission: "read", container_only: false, allowed_actions: ["organization.read"] }], ...overrides };
}

function repositoriesFixture() {
  return { repositories: [{ repository: { id: repoId, organization_id: orgId, name: "issue-spec-e2e", display_name: "Issue Spec E2E", visibility: "private", contribution_policy: "members" }, effective_permission: "read", allowed_actions: ["read"] }] };
}
