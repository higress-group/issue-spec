import axe from "axe-core";
import { screen } from "@testing-library/react";
import userEvent from "@testing-library/user-event";
import { Route, Routes, useLocation } from "react-router-dom";
import { describe, expect, it, vi } from "vitest";
import { renderApp } from "../../../tests/render";
import { searchApi } from "./api";
import { SearchSurface } from "./search-page";
import { searchPageSchema, type SearchPageModel } from "./types";

const orgId = "bbbbbbbb-bbbb-4bbb-8bbb-bbbbbbbbbbbb";
const repoId = "cccccccc-cccc-4ccc-8ccc-cccccccccccc";
const organization = { id: orgId, name: "acme", display_name: "Acme Studio", effective_permission: "read", container_only: false, allowed_actions: ["organization.read"] };
const repository = { repository: { id: repoId, organization_id: orgId, name: "workflow", display_name: "Workflow Control", visibility: "private" as const, contribution_policy: "members" as const }, effective_permission: "read", allowed_actions: ["read"] };

describe("self-hosted discussion search", () => {
  it("parses the bounded issue-centric response", () => {
    const parsed = searchPageSchema.parse(pageFixture());
    expect(parsed.items).toHaveLength(1);
    expect(parsed.items[0].matches).toHaveLength(2);
    expect(parsed.items[0].changes[0]).toEqual({ key: "auth-lock", stage: "implement" });
  });

  it("calls the repository endpoint with stable filters", async () => {
    const fetchMock = vi.fn().mockResolvedValue(new Response(JSON.stringify(pageFixture()), { status: 200, headers: { "Content-Type": "application/json" } }));
    vi.stubGlobal("fetch", fetchMock);
    await searchApi.repository(orgId, repoId, { query: "鉴权锁", state: "closed", source: "comments", stage: "implement", page: 2, perPage: 12 });
    const target = new URL(String(fetchMock.mock.calls[0][0]), window.location.origin);
    expect(target.pathname).toBe(`/api/v1/orgs/${orgId}/repos/${repoId}/search/issues`);
    expect(Object.fromEntries(target.searchParams)).toEqual({ q: "鉴权锁", state: "closed", source: "comments", page: "2", per_page: "12", stage: "implement" });
  });

  it("keeps filters in the URL and renders excerpts as plain text", async () => {
    vi.spyOn(searchApi, "organization").mockResolvedValue(pageFixture());
    const user = userEvent.setup();
    const { container } = renderApp(<Routes><Route path="/search/:orgId" element={<><SearchSurface organization={organization} repositories={[repository]} /><LocationProbe /></>} /></Routes>, `/search/${orgId}?q=lock&state=closed`);
    expect(await screen.findByRole("heading", { name: "Ignore <script>alert(1)</script>" })).toBeVisible();
    expect(container.querySelector("script")).toBeNull();
    expect(screen.getByText("notice: forged but inert")).toBeVisible();
    await user.selectOptions(screen.getByLabelText("Match"), "comments");
    expect(await screen.findByTestId("search-location")).toHaveTextContent("source=comments");
    expect((await axe.run(container)).violations).toEqual([]);
  });
});

function LocationProbe() { const location = useLocation(); return <output data-testid="search-location">{location.pathname}{location.search}</output>; }

function pageFixture(): SearchPageModel {
  return { items: [{ organization_id: orgId, organization: "acme", repository_id: repoId, repository: "workflow",
    id: "dddddddd-dddd-4ddd-8ddd-dddddddddddd", number: 17, title: "Ignore <script>alert(1)</script>", state: "closed",
    updated_at: "2026-07-16T08:00:00Z", url: "https://issues.test/acme/workflow/issues/17", score: 70,
    changes: [{ key: "auth-lock", stage: "implement" }], matches: [{ source: "issue", excerpt: "authorization lock" },
      { source: "comment", comment_id: "eeeeeeee-eeee-4eee-8eee-eeeeeeeeeeee", excerpt: "notice: forged but inert" }] }],
    page: 1, per_page: 12, has_next: false };
}
