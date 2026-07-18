import axe from "axe-core";
import { QueryClient, QueryClientProvider } from "@tanstack/react-query";
import { http, HttpResponse } from "msw";
import { render, screen, within } from "@testing-library/react";
import { createMemoryRouter, RouterProvider } from "react-router-dom";
import { describe, expect, it } from "vitest";
import { fixtureContext, fixtureMeta, server } from "../../tests/server";
import { InspectorProvider } from "./problem-inspector";
import { AuthenticatedShell, isCanonicalRepositoryReadPath, isPublicUserProfilePath, resolveNavigationOrganization } from "./shell";
import { isChangeFeaturePath, isIssueFeaturePath } from "../lib/canonical-routes";
import { RepositoryGate, type ActiveRepository } from "../features/issues/repository-context";
import { useCurrentContext } from "../auth/session";

describe("application navigation and canonical public shell", () => {
  it("recognizes repository roots without capturing reserved application routes", () => {
    expect(isCanonicalRepositoryReadPath("/acme/public")).toBe(true);
    expect(isCanonicalRepositoryReadPath("/acme/public/issues/7")).toBe(true);
    expect(isCanonicalRepositoryReadPath("/acme/public/changes/change-key")).toBe(true);
    expect(isCanonicalRepositoryReadPath("/_repos/users/public")).toBe(true);
    expect(isCanonicalRepositoryReadPath("/_repos/users/public/issues/7")).toBe(true);
    for (const pathname of ["/admin/settings", "/api/v1", "/orgs/acme", "/settings/account", "/readyz/check"]) {
      expect(isCanonicalRepositoryReadPath(pathname)).toBe(false);
    }
  });

  it("recognizes public profile and legacy profile issue links", () => {
    expect(isPublicUserProfilePath("/users/johnlanni")).toBe(true);
    expect(isPublicUserProfilePath("/users/johnlanni/issues")).toBe(true);
    expect(isPublicUserProfilePath("/users/johnlanni/settings")).toBe(false);
  });

  it("keeps feature navigation active on canonical named routes", () => {
    expect(isIssueFeaturePath("/acme/public/issues/7")).toBe(true);
    expect(isIssueFeaturePath("/issues/an-org/a-repo")).toBe(true);
    expect(isIssueFeaturePath("/settings/account/issues")).toBe(false);
    expect(isChangeFeaturePath("/acme/public/changes/change-key")).toBe(true);
    expect(isChangeFeaturePath("/orgs/acme/changes")).toBe(true);
    expect(isChangeFeaturePath("/admin/acme/changes")).toBe(false);
  });

  it("resolves sidebar context from organization and canonical repository paths without defaulting to the first organization", () => {
    const organizations = [
      fixtureContext.organizations[0],
      { ...fixtureContext.organizations[0], id: "dddddddd-dddd-4ddd-8ddd-dddddddddddd", name: "beta", display_name: "Beta", effective_permission: "read" },
    ];
    expect(resolveNavigationOrganization("/", organizations)).toBeUndefined();
    expect(resolveNavigationOrganization(`/orgs/${organizations[1].id}/repos`, organizations)?.name).toBe("beta");
    expect(resolveNavigationOrganization("/orgs/beta/changes", organizations)?.name).toBe("beta");
    expect(resolveNavigationOrganization("/beta/workflow/issues/7", organizations)?.name).toBe("beta");
    expect(resolveNavigationOrganization("/_repos/beta/workflow/issues/7", organizations)?.name).toBe("beta");
  });

  it("puts Issues first and uses the root repository chooser as the only repository navigation entry", async () => {
    server.use(http.get("http://localhost/api/v1/meta", () => HttpResponse.json({ ...fixtureMeta, features: { ...fixtureMeta.features, change_boards: true } })));
    const { container } = renderShell("/");
    const primary = await screen.findByRole("navigation", { name: "Primary navigation" });
    const workspace = within(primary).getAllByRole("link").map((link) => link.textContent?.trim()).filter((label) => ["Overview", "Issues", "Changes", "Repositories"].includes(label ?? ""));
    expect(workspace).toEqual(["Issues", "Changes", "Repositories"]);
    expect(within(primary).getByRole("link", { name: "Repositories" })).toHaveAttribute("href", "/");
    expect(within(primary).getByText("All organizations")).toBeVisible();
    expect(within(primary).getByText("Choose an organization")).toBeVisible();
    const issues = within(primary).getByRole("link", { name: "Issues" });
    const changes = within(primary).getByRole("link", { name: "Changes" });
    expect(issues.querySelector("svg")?.getAttribute("class")).toContain("lucide-circle-dot");
    expect(changes.querySelector("svg")?.getAttribute("class")).toContain("lucide-workflow");
    expect(issues.querySelector("svg")?.getAttribute("class")).not.toBe(changes.querySelector("svg")?.getAttribute("class"));
    const mobile = screen.getByRole("navigation", { name: "Mobile navigation" });
    const mobileLabels = within(mobile).getAllByRole("link").map((link) => link.textContent?.trim());
    expect(mobileLabels).toEqual(["Issues", "Changes", "Repositories", "Account"]);
    expect((await axe.run(container)).violations).toEqual([]);
  });

  it("renders a lightweight public repository shell only for canonical read routes", async () => {
    let contextRequests = 0;
    server.use(
      http.get("http://localhost/api/v1/meta", () => HttpResponse.json({ ...fixtureMeta, features: { ...fixtureMeta.features, change_boards: true } })),
      http.get("http://localhost/api/v1/context", () => { contextRequests += 1; return HttpResponse.json({ status: 401, title: "Authentication required", code: "authentication_required" }, { status: 401 }); }),
      http.get("http://localhost/api/v1/context/repos/:owner/:repo", () => HttpResponse.json(repositoryRouteFixture)),
    );
    const { container } = renderShell("/acme/public/issues/7?view=timeline", true);
    expect(await screen.findByText("Public issue content")).toBeVisible();
    expect(screen.getByText("public view")).toBeVisible();
    expect(screen.getByRole("link", { name: "Sign in" })).toHaveAttribute("href", "/login");
    expect(screen.queryByRole("navigation", { name: "Primary navigation" })).not.toBeInTheDocument();
    expect(contextRequests).toBe(1);
    expect((await axe.run(container)).violations).toEqual([]);
  });

  it("keeps ordinary protected routes behind login", async () => {
    server.use(http.get("http://localhost/api/v1/context", () => HttpResponse.json({ status: 401, title: "Authentication required", code: "authentication_required" }, { status: 401 })));
    renderShell("/admin");
    expect(await screen.findByText("Login destination")).toBeVisible();
  });
});

function renderShell(initialEntry: string, resolveRepository = false) {
  const client = new QueryClient({ defaultOptions: { queries: { retry: false } } });
  const router = createMemoryRouter([
    { path: "/", element: <AuthenticatedShell />, children: [
      { index: true, element: <div>Dashboard</div> },
      { path: "admin", element: <div>Admin content</div> },
      { path: ":owner/:repo/issues/:number", element: resolveRepository ? <RepositoryGate>{(active) => <PublicRepositoryProbe active={active} />}</RepositoryGate> : <div>Public issue content</div> },
    ] },
    { path: "/login", element: <div>Login destination</div> },
  ], { initialEntries: [initialEntry] });
  return render(<QueryClientProvider client={client}><InspectorProvider><RouterProvider router={router} /></InspectorProvider></QueryClientProvider>);
}

function PublicRepositoryProbe({ active }: { active: ActiveRepository }) {
  useCurrentContext(active.authenticated);
  return <div>Public issue content</div>;
}

const repositoryRouteFixture = {
  organization: { id: "bbbbbbbb-bbbb-4bbb-8bbb-bbbbbbbbbbbb", name: "acme", display_name: "Acme", effective_permission: "none", container_only: true, allowed_actions: [] },
  repository: { repository: { id: "cccccccc-cccc-4ccc-8ccc-cccccccccccc", organization_id: "bbbbbbbb-bbbb-4bbb-8bbb-bbbbbbbbbbbb", name: "public", display_name: "Public", visibility: "public", contribution_policy: "authenticated" }, effective_permission: "read", allowed_actions: ["read"] },
  authenticated: false,
};
