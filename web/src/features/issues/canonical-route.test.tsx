import axe from "axe-core";
import { QueryClient, QueryClientProvider } from "@tanstack/react-query";
import { http, HttpResponse } from "msw";
import { render, screen } from "@testing-library/react";
import { createMemoryRouter, Link, RouterProvider, useLocation } from "react-router-dom";
import { describe, expect, it } from "vitest";
import { server } from "../../../tests/server";
import contribution from "./contribution";
import { RepositoryGate, repositoryChangePath, repositoryIssuePath, type ActiveRepository } from "./repository-context";

const orgId = "bbbbbbbb-bbbb-4bbb-8bbb-bbbbbbbbbbbb";
const repoId = "cccccccc-cccc-4ccc-8ccc-cccccccccccc";

describe("canonical repository Web routes", () => {
  it("registers stable issue list, create and detail routes without a UUID redirect adapter", () => {
    const paths = contribution.routes?.map((route) => route.path);
    expect(paths).toEqual(expect.arrayContaining([":owner/:repo/issues", ":owner/:repo/issues/new", ":owner/:repo/issues/:number"]));
    expect(paths).not.toContain(":owner/:repo/issues/:issueNumber");
  });

  it("keeps the canonical URL and generates canonical issue/change links for an anonymous public repository", async () => {
    server.use(
      http.get("http://localhost/api/v1/context", () => HttpResponse.json({ status: 401, title: "Authentication required", code: "authentication_required" }, { status: 401 })),
      http.get("http://localhost/api/v1/context/repos/:owner/:repo", ({ params }) => {
        expect(params).toMatchObject({ owner: "BROWSER-E2E", repo: "ISSUE-SPEC-E2E" });
        return HttpResponse.json(repositoryRouteFixture(false));
      }),
    );
    const { container, router } = renderCanonical("/BROWSER-E2E/ISSUE-SPEC-E2E/issues/2?view=timeline#issuecomment-9");
    expect(await screen.findByTestId("resolved-location")).toHaveTextContent("/BROWSER-E2E/ISSUE-SPEC-E2E/issues/2?view=timeline#issuecomment-9");
    expect(router.state.location.pathname).toBe("/BROWSER-E2E/ISSUE-SPEC-E2E/issues/2");
    expect(screen.getByRole("link", { name: "Issue list" })).toHaveAttribute("href", "/browser-e2e/issue-spec-e2e/issues");
    expect(screen.getByRole("link", { name: "Change board" })).toHaveAttribute("href", "/browser-e2e/issue-spec-e2e/changes");
    expect((await axe.run(container)).violations).toEqual([]);
  });

  it("conceals private anonymous context with one 404 state", async () => {
    server.use(
      http.get("http://localhost/api/v1/context", () => HttpResponse.json({ status: 401, title: "Authentication required", code: "authentication_required" }, { status: 401 })),
      http.get("http://localhost/api/v1/context/repos/:owner/:repo", () => HttpResponse.json({ status: 404, title: "Not found", code: "not_found" }, { status: 404 })),
    );
    renderCanonical("/acme/private/issues/2");
    expect(await screen.findByRole("heading", { name: "That issue desk is not here" })).toBeVisible();
    expect(screen.queryByTestId("repository-content")).not.toBeInTheDocument();
  });

  it("does not downgrade an invalid credential into anonymous public access", async () => {
    server.use(
      http.get("http://localhost/api/v1/context", () => HttpResponse.json({ status: 401, title: "Authentication required", code: "authentication_required" }, { status: 401 })),
      http.get("http://localhost/api/v1/context/repos/:owner/:repo", () => HttpResponse.json({ status: 401, title: "Authentication required", code: "authentication_required" }, { status: 401 })),
    );
    renderCanonical("/acme/public/issues/2");
    expect(await screen.findByRole("heading", { name: "Sign in to continue" })).toBeVisible();
    expect(screen.queryByTestId("repository-content")).not.toBeInTheDocument();
  });
});

function renderCanonical(initialEntry: string) {
  const client = new QueryClient({ defaultOptions: { queries: { retry: false } } });
  const router = createMemoryRouter([{ path: "/:owner/:repo/issues/:number", element: <RepositoryGate>{(active) => <RepositoryProbe active={active} />}</RepositoryGate> }], { initialEntries: [initialEntry] });
  return { router, ...render(<QueryClientProvider client={client}><RouterProvider router={router} /></QueryClientProvider>) };
}

function RepositoryProbe({ active }: { active: ActiveRepository }) {
  const location = useLocation();
  return <main data-testid="repository-content"><output data-testid="resolved-location">{location.pathname}{location.search}{location.hash}</output><Link to={repositoryIssuePath(active)}>Issue list</Link><Link to={repositoryChangePath(active)}>Change board</Link></main>;
}

function repositoryRouteFixture(authenticated: boolean) {
  return {
    organization: { id: orgId, name: "browser-e2e", display_name: "Browser E2E", effective_permission: "read", container_only: true, allowed_actions: [] },
    repository: { repository: { id: repoId, organization_id: orgId, name: "issue-spec-e2e", display_name: "Issue Spec E2E", visibility: "public", contribution_policy: "authenticated" }, effective_permission: "read", allowed_actions: ["read"] },
    authenticated,
  };
}
