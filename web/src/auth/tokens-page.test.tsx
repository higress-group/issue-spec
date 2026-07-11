import axe from "axe-core";
import { http, HttpResponse } from "msw";
import { screen, waitFor } from "@testing-library/react";
import userEvent from "@testing-library/user-event";
import { describe, expect, it } from "vitest";
import { renderApp } from "../../tests/render";
import { server } from "../../tests/server";
import { TokensPage } from "./tokens-page";

const orgId = "bbbbbbbb-bbbb-4bbb-8bbb-bbbbbbbbbbbb";
const repoId = "cccccccc-cccc-4ccc-8ccc-cccccccccccc";

describe("personal access token repository boundaries", () => {
  it("creates an exactly repository-scoped runner parent token", async () => {
    let created: unknown;
    server.use(...handlers(), http.post("http://localhost/api/v1/pats", async ({ request }) => {
      created = await request.json();
      return HttpResponse.json({ token: "pat-show-once" }, { status: 201 });
    }));
    const { container } = renderApp(<TokensPage />, "/settings/tokens");

    await userEvent.setup().type(await screen.findByRole("textbox", { name: "Token name" }), "local runner");
    await userEvent.setup().click(screen.getByRole("button", { name: "Runner preset" }));
    await userEvent.setup().selectOptions(screen.getByRole("combobox", { name: "Repository access" }), repoId);
    await userEvent.setup().click(screen.getByRole("button", { name: "Create token" }));

    await waitFor(() => expect(created).toEqual({
      name: "local runner",
      scopes: ["read:user", "issues:read", "issues:write", "runner:delegate"],
      repositories: [{ organization_id: orgId, repository_id: repoId }],
      expires_at: null,
    }));
    expect(await screen.findByRole("dialog", { name: "Save this access token" })).toHaveTextContent("pat-show-once");
    expect((await axe.run(container)).violations).toEqual([]);
  });

  it("requires a repository for delegation and names persisted boundaries", async () => {
    let posts = 0;
    server.use(...handlers(), http.post("http://localhost/api/v1/pats", () => { posts += 1; return HttpResponse.json({ token: "unexpected" }, { status: 201 }); }));
    renderApp(<TokensPage />, "/settings/tokens");

    expect(await screen.findByText("Restricted to browser-e2e/issue-spec-e2e")).toBeVisible();
    await userEvent.setup().type(screen.getByRole("textbox", { name: "Token name" }), "unsafe runner");
    await userEvent.setup().click(screen.getByRole("button", { name: "Runner preset" }));
    await userEvent.setup().click(screen.getByRole("button", { name: "Create token" }));

    expect(await screen.findByText("Runner delegation requires exactly one repository")).toBeVisible();
    expect(posts).toBe(0);
  });

  it("separates revoked history from actionable active credentials", async () => {
    server.use(http.get("http://localhost/api/v1/pats", () => HttpResponse.json({ tokens: [
      { id: "dddddddd-dddd-4ddd-8ddd-dddddddddddd", name: "active runner", token_prefix: "pat_live", scopes: ["runner:delegate"], repositories: [{ organization_id: orgId, repository_id: repoId }], repository_restricted: true, representation_version: 1 },
      { id: "eeeeeeee-eeee-4eee-8eee-eeeeeeeeeeee", name: "retired runner", token_prefix: "pat_old", scopes: ["runner:delegate"], repositories: [{ organization_id: orgId, repository_id: repoId }], repository_restricted: true, revoked_at: "2026-07-11T11:00:00Z", representation_version: 2 },
    ] })), ...handlers());
    const { container } = renderApp(<TokensPage />, "/settings/tokens");

    expect(await screen.findByText("1 usable credential")).toBeVisible();
    expect(screen.getByText("1 retired credential retained for audit")).toBeVisible();
    expect(screen.getByRole("button", { name: "Rotate active runner" })).toBeVisible();
    expect(screen.getByRole("button", { name: "Revoke active runner" })).toBeVisible();
    expect(screen.queryByRole("button", { name: "Rotate retired runner" })).not.toBeInTheDocument();
    expect(screen.queryByRole("button", { name: "Revoke retired runner" })).not.toBeInTheDocument();
    expect(screen.getByText("Revoked")).toBeVisible();
    expect((await axe.run(container)).violations).toEqual([]);
  });
});

function handlers() {
  return [
    http.get("http://localhost/api/v1/context", () => HttpResponse.json({
      user: { id: "aaaaaaaa-aaaa-4aaa-8aaa-aaaaaaaaaaaa", login: "browser-admin", display_name: "Browser Admin", site_admin: true },
      credential: { kind: "session", scope_mode: "identity", repository_restricted: false },
      session: { csrf_cookie_name: "issue_spec_csrf", csrf_header_name: "X-CSRF-Token" },
      allowed_actions: ["site.admin"],
      organizations: [{ id: orgId, name: "browser-e2e", display_name: "Browser E2E", effective_permission: "admin", container_only: false, allowed_actions: ["organization.admin"] }],
    })),
    http.get(`http://localhost/api/v1/context/orgs/${orgId}/repos`, () => HttpResponse.json({ repositories: [{ repository: { id: repoId, organization_id: orgId, name: "issue-spec-e2e", display_name: "Issue Spec E2E", visibility: "private", contribution_policy: "members" }, effective_permission: "admin", allowed_actions: ["repository.admin"] }] })),
    http.get("http://localhost/api/v1/pats", () => HttpResponse.json({ tokens: [{ id: "dddddddd-dddd-4ddd-8ddd-dddddddddddd", name: "existing runner", token_prefix: "pat_demo", scopes: ["runner:delegate"], repositories: [{ organization_id: orgId, repository_id: repoId }], repository_restricted: true, representation_version: 1 }] })),
  ];
}
