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
  it("creates a repository-restricted managed PAT for a service account", async () => {
    const capture = vi.fn();
    server.use(
      http.get(`http://localhost/api/v1/orgs/${orgId}/repos`, () => HttpResponse.json({ repositories: [{ id: repoId, organization_id: orgId, name: "workflow", display_name: "Workflow", visibility: "private", default_branch: "main", contribution_policy: "members", representation_version: 1 }] })),
      http.get(`http://localhost/api/v1/orgs/${orgId}/user-candidates`, () => HttpResponse.json({ users: [{ id: serviceAccountId, login: "svc-runner-bot-a1b2c3d4", display_name: "Runner Bot", kind: "service_account", status: "active" }] })),
      http.get(`http://localhost/api/v1/orgs/${orgId}/users/${serviceAccountId}/pats`, () => HttpResponse.json({ tokens: [] })),
      http.post(`http://localhost/api/v1/orgs/${orgId}/users/${serviceAccountId}/pats`, async ({ request }) => { capture(await request.json()); return HttpResponse.json({ token: "shown-once" }, { status: 201 }); }),
    );
    renderApp(<Routes><Route path="/admin/orgs/:orgId/managed-tokens" element={<ManagedTokensPage />} /></Routes>, `/admin/orgs/${orgId}/managed-tokens`);
    const user = userEvent.setup();
    await user.type(await screen.findByRole("textbox", { name: "Exact local login" }), "svc-runner-bot-a1b2c3d4");
    await user.click(screen.getByRole("button", { name: "Resolve" }));
    expect(await screen.findByText("@svc-runner-bot-a1b2c3d4")).toBeVisible();
    await user.click(screen.getByRole("button", { name: "Runner preset" }));
    await user.selectOptions(screen.getByRole("combobox", { name: "Repository access" }), repoId);
    await user.click(screen.getByRole("button", { name: "Create scoped token" }));
    expect(await screen.findByRole("dialog")).toBeVisible();
    expect(capture).toHaveBeenCalledWith({
      name: "runner",
      scopes: ["read:user", "issues:read", "issues:write", "runner:delegate", "evidence:write"],
      repository_ids: [repoId],
    });
  });
});
