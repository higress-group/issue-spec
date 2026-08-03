import { http, HttpResponse } from "msw";
import { screen } from "@testing-library/react";
import userEvent from "@testing-library/user-event";
import { Route, Routes } from "react-router-dom";
import { describe, expect, it, vi } from "vitest";
import { renderApp } from "../../tests/render";
import { server } from "../../tests/server";
import { ManagedTokensPage, ServiceAccountsPage } from "./service-accounts-page";

const orgId = "bbbbbbbb-bbbb-4bbb-8bbb-bbbbbbbbbbbb";
const repoId = "cccccccc-cccc-4ccc-8ccc-cccccccccccc";
const secondRepoId = "ffffffff-ffff-4fff-8fff-ffffffffffff";
const serviceAccountId = "dddddddd-dddd-4ddd-8ddd-dddddddddddd";

describe("service account response compatibility", () => {
  it("renders an empty account list when the server returns null", async () => {
    server.use(http.get(`http://localhost/api/v1/orgs/${orgId}/service-accounts`, () => HttpResponse.json({ service_accounts: null })));
    renderApp(<Routes><Route path="/admin/orgs/:orgId/service-accounts" element={<ServiceAccountsPage />} /></Routes>, `/admin/orgs/${orgId}/service-accounts`);
    expect(await screen.findByRole("heading", { name: "Service accounts" })).toBeVisible();
    expect(screen.queryByRole("alert")).not.toBeInTheDocument();
  });
});

describe("managed runner credentials", () => {
  it("creates a site-wide managed PAT by default", async () => {
    const capture = vi.fn();
    server.use(...managedTokenHandlers(capture, [repository(repoId, "workflow")]));
    renderApp(<Routes><Route path="/admin/orgs/:orgId/managed-tokens" element={<ManagedTokensPage />} /></Routes>, `/admin/orgs/${orgId}/managed-tokens`);
    const user = userEvent.setup();
    await user.type(await screen.findByRole("textbox", { name: "Exact local login" }), "svc-runner-bot-a1b2c3d4");
    await user.click(screen.getByRole("button", { name: "Resolve" }));
    expect(await screen.findByText("@svc-runner-bot-a1b2c3d4")).toBeVisible();
    await user.type(screen.getByRole("textbox", { name: "Token name" }), "site-wide automation");
    expect(screen.getByRole("radio", { name: "All repositories (site-wide)" })).toBeChecked();
    await user.click(screen.getByRole("button", { name: "Create scoped token" }));
    expect(await screen.findByRole("dialog")).toBeVisible();
    expect(capture).toHaveBeenCalledWith({
      name: "site-wide automation",
      scopes: ["read:user", "read:org", "repo", "issues:read", "issues:write", "admin:org", "admin:repo", "evidence:write", "runner:delegate"],
      repository_ids: [],
    });
  });

  it("defaults to all permissions and creates a multi-repository managed PAT", async () => {
    const capture = vi.fn();
    server.use(...managedTokenHandlers(capture, [repository(repoId, "workflow"), repository(secondRepoId, "secondary")]));
    renderApp(<Routes><Route path="/admin/orgs/:orgId/managed-tokens" element={<ManagedTokensPage />} /></Routes>, `/admin/orgs/${orgId}/managed-tokens`);
    const user = userEvent.setup();
    await user.type(await screen.findByRole("textbox", { name: "Exact local login" }), "svc-runner-bot-a1b2c3d4");
    await user.click(screen.getByRole("button", { name: "Resolve" }));
    expect(await screen.findByText("@svc-runner-bot-a1b2c3d4")).toBeVisible();
    expect(screen.getAllByRole("checkbox")).toHaveLength(9);
    screen.getAllByRole("checkbox").forEach((checkbox) => expect(checkbox).toBeChecked());
    await user.click(screen.getByRole("button", { name: "Runner preset" }));
    await user.click(screen.getByRole("radio", { name: "Selected repositories" }));
    await user.click(screen.getByRole("button", { name: "Create scoped token" }));
    expect(await screen.findByRole("dialog")).toBeVisible();
    expect(capture).toHaveBeenCalledWith({
      name: "runner",
      scopes: ["read:user", "issues:read", "issues:write"],
      repository_ids: [repoId, secondRepoId],
    });
  });
});

function repository(id: string, name: string) {
  return { id, organization_id: orgId, name, display_name: name, visibility: "private", default_branch: "main", contribution_policy: "members", representation_version: 1 };
}

function managedTokenHandlers(capture: (body: unknown) => void, repositories: ReturnType<typeof repository>[]) {
  return [
    http.get(`http://localhost/api/v1/orgs/${orgId}/repos`, () => HttpResponse.json({ repositories })),
    http.get(`http://localhost/api/v1/orgs/${orgId}/user-candidates`, () => HttpResponse.json({ users: [{ id: serviceAccountId, login: "svc-runner-bot-a1b2c3d4", display_name: "Runner Bot", kind: "service_account", status: "active" }] })),
    http.get(`http://localhost/api/v1/orgs/${orgId}/users/${serviceAccountId}/pats`, () => HttpResponse.json({ tokens: [] })),
    http.post(`http://localhost/api/v1/orgs/${orgId}/users/${serviceAccountId}/pats`, async ({ request }) => { capture(await request.json()); return HttpResponse.json({ token: "shown-once" }, { status: 201 }); }),
  ];
}
