import axe from "axe-core";
import { screen } from "@testing-library/react";
import userEvent from "@testing-library/user-event";
import { Route, Routes, useLocation } from "react-router-dom";
import { describe, expect, it, vi } from "vitest";
import { renderApp } from "../../../tests/render";
import i18n from "../../i18n/i18n";
import { searchApi } from "./api";
import { groupSearchResults, SearchSurface } from "./search-page";
import { searchPageSchema, type SearchPageModel } from "./types";

const orgId = "bbbbbbbb-bbbb-4bbb-8bbb-bbbbbbbbbbbb";
const repoId = "cccccccc-cccc-4ccc-8ccc-cccccccccccc";
const organization = { id: orgId, name: "acme", display_name: "Acme Studio", effective_permission: "read", container_only: false, allowed_actions: ["organization.read"] };
const repository = { repository: { id: repoId, organization_id: orgId, name: "workflow", display_name: "Workflow Control", visibility: "private" as const, contribution_policy: "members" as const }, effective_permission: "read", allowed_actions: ["read"] };

describe("self-hosted Proposal search", () => {
  it("parses the bounded Proposal response", () => {
    const parsed = searchPageSchema.parse(pageFixture());
    expect(parsed.items).toHaveLength(1);
    expect(parsed.items[0].matches).toHaveLength(1);
    expect(parsed.items[0].changes[0]).toEqual({ key: "auth-lock", stage: "proposal", matched: false });
  });

  it("calls the repository endpoint with stable filters", async () => {
    const fetchMock = vi.fn().mockResolvedValue(new Response(JSON.stringify(pageFixture()), { status: 200, headers: { "Content-Type": "application/json" } }));
    vi.stubGlobal("fetch", fetchMock);
    await searchApi.repository(orgId, repoId, { query: "鉴权锁", state: "closed", page: 2, perPage: 12 });
    const target = new URL(String(fetchMock.mock.calls[0][0]), window.location.origin);
    expect(target.pathname).toBe(`/api/v1/orgs/${orgId}/repos/${repoId}/search/issues`);
    expect(Object.fromEntries(target.searchParams)).toEqual({ q: "鉴权锁", state: "closed", page: "2", per_page: "12" });
  });

  it("groups multi-change results only by an explicit match", () => {
    const item = pageFixture().items[0];
    const exact = { ...item, changes: [{ key: "legacy", stage: "proposal" as const, matched: false }, { key: "auth-lock", stage: "proposal" as const, matched: true }] };
    const ambiguous = { ...item, id: "aaaaaaaa-aaaa-4aaa-8aaa-aaaaaaaaaaaa", changes: [{ key: "alpha", stage: "proposal" as const, matched: false }, { key: "beta", stage: "proposal" as const, matched: false }] };
    const groups = groupSearchResults([exact, ambiguous]);
    expect(groups[0].change?.key).toBe("auth-lock");
    expect(groups[1].id).toBe(`issue:${ambiguous.id}`);
    expect(groups[1].change).toBeUndefined();
  });

  it("keeps filters in the URL and renders excerpts as plain text", async () => {
    vi.spyOn(searchApi, "organization").mockResolvedValue(groupedPageFixture());
    const user = userEvent.setup();
    const { container } = renderApp(<Routes><Route path="/search/:orgId" element={<><SearchSurface organization={organization} repositories={[repository]} /><LocationProbe /></>} /></Routes>, `/search/${orgId}?q=lock&state=closed`);
    expect(await screen.findByRole("heading", { name: "Ignore <script>alert(1)</script>" })).toBeVisible();
    expect(container.querySelector("script")).toBeNull();
    expect(screen.getByRole("region", { name: "auth-lock change" })).toBeVisible();
    expect(screen.getAllByText("1 matching artifact · proposal")).toHaveLength(3);
    expect(screen.getByRole("heading", { name: "Proposal: Retry policy" })).toBeVisible();
    expect(screen.queryByLabelText("Match")).not.toBeInTheDocument();
    expect(screen.queryByLabelText("Change stage")).not.toBeInTheDocument();
    await user.selectOptions(screen.getByLabelText("State"), "open");
    expect(await screen.findByTestId("search-location")).toHaveTextContent("state=open");
    expect((await axe.run(container)).violations).toEqual([]);
  });

  it("translates the complete search surface into Chinese", async () => {
    await i18n.changeLanguage("zh-CN");
    vi.spyOn(searchApi, "organization").mockResolvedValue(groupedPageFixture());
    const { container } = renderApp(<Routes><Route path="/search/:orgId" element={<SearchSurface organization={organization} repositories={[repository]} />}/></Routes>, `/search/${orgId}?q=lock&state=closed`);
    expect(await screen.findByRole("heading", { name: "3 个匹配 Proposal" })).toBeVisible();
    expect(screen.getByLabelText("检索范围")).toBeVisible();
    expect(screen.queryByLabelText("匹配内容")).not.toBeInTheDocument();
    expect(screen.getByRole("region", { name: "auth-lock 变更" })).toBeVisible();
    expect(screen.getAllByText("1 个匹配产物 · 提议")).toHaveLength(3);
    expect(screen.getAllByText("已关闭").length).toBeGreaterThan(0);
    expect(screen.getAllByText("Proposal").length).toBeGreaterThan(0);
    expect(screen.getAllByText("打开 Proposal").length).toBeGreaterThan(0);
    expect(screen.queryByText("Open Proposal")).not.toBeInTheDocument();
    expect((await axe.run(container)).violations).toEqual([]);
  });
});

function LocationProbe() { const location = useLocation(); return <output data-testid="search-location">{location.pathname}{location.search}</output>; }

function pageFixture(): SearchPageModel {
  return { items: [{ organization_id: orgId, organization: "acme", repository_id: repoId, repository: "workflow",
    id: "dddddddd-dddd-4ddd-8ddd-dddddddddddd", number: 17, title: "Ignore <script>alert(1)</script>", state: "closed",
    updated_at: "2026-07-16T08:00:00Z", url: "https://issues.test/acme/workflow/issues/17", score: 70,
    changes: [{ key: "auth-lock", stage: "proposal", matched: false }], matches: [{ source: "issue", excerpt: "authorization lock" }] }],
    page: 1, per_page: 12, total: 1, has_next: false };
}

function groupedPageFixture(): SearchPageModel {
  const page = pageFixture();
  const proposal = { ...page.items[0], changes: [{ key: "auth-lock", stage: "proposal" as const, matched: false }] };
  return { ...page, total: 3, items: [proposal,
    { ...page.items[0], id: "aaaaaaaa-aaaa-4aaa-8aaa-aaaaaaaaaaaa", number: 18, title: "Proposal: Retry policy", changes: [{ key: "retry-policy", stage: "proposal", matched: false }], matches: [{ source: "issue", excerpt: "retry proposal" }] },
    { ...page.items[0], id: "ffffffff-ffff-4fff-8fff-ffffffffffff", number: 19, title: "Proposal: Request limits", changes: [{ key: "request-limits", stage: "proposal", matched: false }], matches: [{ source: "issue", excerpt: "request limit proposal" }] },
  ] };
}
