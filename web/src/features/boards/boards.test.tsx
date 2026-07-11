import axe from "axe-core";
import { screen } from "@testing-library/react";
import userEvent from "@testing-library/user-event";
import { Route, Routes } from "react-router-dom";
import { describe, expect, it, vi } from "vitest";
import { renderApp } from "../../../tests/render";
import { boardApi } from "./api";
import { BoardListPage, SafeBoardState } from "./board-page";
import { ChangeCard } from "./components";
import { anomalyCopy, boardPageSchema, type BoardPageModel, type ChangeCardModel } from "./types";

const orgId = "bbbbbbbb-bbbb-4bbb-8bbb-bbbbbbbbbbbb";
const repoId = "cccccccc-cccc-4ccc-8ccc-cccccccccccc";

describe("change board projection UI", () => {
  it("renders one accessible card with distinct valid, invalid, and missing artifact states", async () => {
    const card = cardFixture();
    const { container } = renderApp(<ChangeCard card={card} orgId={orgId} />);
    expect(screen.getAllByRole("article")).toHaveLength(1);
    expect(screen.getByText("Blocked")).toBeVisible();
    expect(screen.getByLabelText(/Proposal artifact, issue 160, valid/)).toBeVisible();
    expect(screen.getByLabelText(/Design artifact, issue 161, invalid/)).toBeVisible();
    expect(screen.getByLabelText(/Implement artifact missing/)).toBeVisible();
    expect(screen.getByText("marker_label_mismatch")).toBeVisible();
    expect((await axe.run(container)).violations).toEqual([]);
  });

  it("maps diagnostic codes to human language while preserving unknown codes", () => {
    expect(anomalyCopy("implement_missing_predecessor").label).toBe("Predecessor missing");
    expect(anomalyCopy("future_server_code")).toMatchObject({ label: "Projection diagnostic" });
  });

  it("parses the native board contract without raw issue bodies", () => {
    const parsed = boardPageSchema.parse(boardFixture());
    expect(parsed.cards).toHaveLength(1);
    expect(parsed.cards[0].artifacts.proposal?.marker_version).toBe("1");
    expect(parsed.cards[0]).not.toHaveProperty("body");
  });

  it("calls the repository endpoint with stable filter and pagination parameters", async () => {
    const fetchMock = vi.fn().mockResolvedValue(new Response(JSON.stringify(boardFixture()), { status: 200, headers: { "Content-Type": "application/json" } }));
    vi.stubGlobal("fetch", fetchMock);
    await boardApi.repositoryBoard(orgId, repoId, { stage: "implement", lifecycle: "blocked", anomaly: "missing_required_links", page: 3, perPage: 12 });
    expect(fetchMock).toHaveBeenCalledOnce();
    const target = new URL(String(fetchMock.mock.calls[0][0]), window.location.origin);
    expect(target.pathname).toBe(`/api/v1/orgs/${orgId}/repos/${repoId}/changes`);
    expect(Object.fromEntries(target.searchParams)).toEqual({ page: "3", per_page: "12", stage: "implement", lifecycle: "blocked", anomaly: "missing_required_links" });
  });

  it("keeps filters in the URL and switches repository scope through feature-owned adapters", async () => {
    vi.spyOn(boardApi, "context").mockResolvedValue(contextFixture);
    vi.spyOn(boardApi, "repositories").mockResolvedValue(repositoryFixture);
    vi.spyOn(boardApi, "organizationBoard").mockResolvedValue(boardFixture());
    vi.spyOn(boardApi, "repositoryBoard").mockResolvedValue(boardFixture());
    const user = userEvent.setup();
    renderApp(<Routes><Route path="/changes/:orgId" element={<BoardListPage />} /><Route path="/changes/:orgId/repos/:repoId" element={<BoardListPage />} /></Routes>, `/changes/${orgId}`);
    expect(await screen.findByRole("heading", { name: "Acme Studio changes" })).toBeVisible();
    await user.selectOptions(screen.getByLabelText("Stage"), "design");
    expect(screen.getByDisplayValue("Design")).toBeVisible();
    await user.selectOptions(screen.getByLabelText("Board scope"), repoId);
    expect(await screen.findByRole("heading", { name: "Workflow Control" })).toBeVisible();
  });

  it("uses one concealed state for missing or unauthorized details", async () => {
    const { container } = renderApp(<SafeBoardState />);
    expect(screen.getByRole("heading", { name: "This change board is not available" })).toBeVisible();
    expect(screen.getByText(/may not exist/)).toBeVisible();
    expect((await axe.run(container)).violations).toEqual([]);
  });
});

const contextFixture = {
  user: { id: "aaaaaaaa-aaaa-4aaa-8aaa-aaaaaaaaaaaa", login: "alice", display_name: "Alice", email: "alice@example.test", site_admin: false },
  credential: { kind: "session", scope_mode: "identity" as const, repository_restricted: false },
  session: { csrf_cookie_name: "issue_spec_csrf", csrf_header_name: "X-CSRF-Token" },
  allowed_actions: [],
  organizations: [{ id: orgId, name: "acme", display_name: "Acme Studio", effective_permission: "read", container_only: false, allowed_actions: ["organization.read"] }],
};

const repositoryFixture = { repositories: [{ repository: { id: repoId, organization_id: orgId, name: "workflow", display_name: "Workflow Control", visibility: "private" as const, contribution_policy: "members" as const }, effective_permission: "read", allowed_actions: ["read"] }] };

function cardFixture(): ChangeCardModel {
  return {
    repository: { id: repoId, name: "workflow", display_name: "Workflow Control" },
    change_key: "self-hosted-board",
    title: "Self-hosted change board",
    current_stage: "design",
    lifecycle: "blocked",
    artifacts: {
      proposal: { id: "11111111-1111-4111-8111-111111111111", number: 160, title: "Proposal", state: "closed", url: `/issues/${orgId}/${repoId}/160`, marker_version: "1", updated_at: "2026-07-10T10:00:00Z", valid: true },
      design: { id: "22222222-2222-4222-8222-222222222222", number: 161, title: "Design", state: "open", url: `/issues/${orgId}/${repoId}/161`, marker_version: "2", updated_at: "2026-07-10T11:00:00Z", valid: false },
    },
    tasks: { total: 5, completed: 2, in_progress: 1, blocked: 1, pending: 1 },
    processes: { total: 3, completed: 1, in_progress: 1, blocked: 0, pending: 1 },
    anomalies: ["marker_label_mismatch"],
    updated_at: "2026-07-10T12:00:00Z",
  };
}

function boardFixture(): BoardPageModel {
  return {
    cards: [cardFixture()], page: 1, per_page: 12, total: 1,
    counts: { total: 1, active: 0, blocked: 1, completed: 0, closed: 0, proposal: 0, design: 1, implement: 0, unknown: 0 },
    diagnostics: [{ code: "orphan_typed_artifact", count: 1 }],
  };
}
