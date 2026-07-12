import AxeBuilder from "@axe-core/playwright";
import { expect, test } from "@playwright/test";
import type { Artifact, ChangeCardModel } from "./types";
import type { CodeChangeRelationship } from "../../lib/api/relationships";

const orgId = "bbbbbbbb-bbbb-4bbb-8bbb-bbbbbbbbbbbb";
const repoId = "cccccccc-cccc-4ccc-8ccc-cccccccccccc";

test.beforeEach(async ({ page }) => {
  await page.emulateMedia({ reducedMotion: "reduce" });
  await page.route("**/*", async (route) => {
    const request = route.request();
    const url = new URL(request.url());
    if (url.pathname === "/api/v1/meta") return route.fulfill({ json: { api_version: "v1", features: { bootstrap: true, personal_access_tokens: true, organizations: true, source_bindings: false, webhooks: false, change_boards: true, runner: true, recovery_exchange: true } } });
    if (url.pathname === "/api/v1/context") return route.fulfill({ json: context });
    if (url.pathname === `/api/v1/context/orgs/${orgId}/repos`) return route.fulfill({ json: repositories });
    if (url.pathname === "/api/v1/context/repos/acme/workflow") return route.fulfill({ json: { organization: context.organizations[0], repository: repositories.repositories[0], authenticated: true } });
    if (url.pathname === `/api/v1/orgs/${orgId}/changes`) return route.fulfill({ json: boardResponse(url, cards) });
    if (url.pathname === `/api/v1/orgs/${orgId}/repos/${repoId}/changes`) return route.fulfill({ json: boardResponse(url, cards) });
    if (url.pathname.startsWith(`/api/v1/orgs/${orgId}/repos/${repoId}/changes/`)) {
      const key = decodeURIComponent(url.pathname.split("/").at(-1) ?? "");
      const card = cards.find((item) => item.change_key === key);
      return card ? route.fulfill({ json: card }) : route.fulfill({ status: 404, json: { type: "about:blank", title: "Not found", status: 404, code: "not_found", request_id: "board-e2e" } });
    }
    return route.fallback();
  });
});

test("change board is responsive, keyboard reachable, accessible, and visually stable", async ({ page }, testInfo) => {
  await page.goto(`/changes/${orgId}/repos/${repoId}`);
  await expect(page.getByRole("heading", { level: 1, name: "Workflow Control" })).toBeVisible();
  await expect(page.getByRole("article")).toHaveCount(4);
  await expect(page.getByLabel(/Change pipeline/).first()).toBeVisible();
  await expect(page.getByLabel(/2 linked code changes/)).toBeVisible();
  const overflow = await page.evaluate(() => ({
    difference: document.documentElement.scrollWidth - document.documentElement.clientWidth,
    offenders: [...document.querySelectorAll<HTMLElement>("body *")].filter((element) => element.getBoundingClientRect().right > document.documentElement.clientWidth + 1).slice(0, 6).map((element) => ({ className: element.className, right: Math.round(element.getBoundingClientRect().right), scrollWidth: element.scrollWidth, clientWidth: element.clientWidth })),
  }));
  expect(overflow.difference, JSON.stringify(overflow.offenders)).toBeLessThanOrEqual(1);
  const accessibility = await new AxeBuilder({ page }).analyze();
  expect(accessibility.violations).toEqual([]);
  await page.evaluate(() => (document.activeElement as HTMLElement | null)?.blur());
  await expect(page).toHaveScreenshot("change-board.png", { fullPage: true, animations: "disabled" });

  if (testInfo.project.name === "boards-desktop-1440") {
    await page.keyboard.press("Tab");
    await expect(page.locator(":focus")).toBeVisible();
    await page.getByLabel("Lifecycle").selectOption("blocked");
    await expect(page).toHaveURL(/lifecycle=blocked/);
    await expect(page.getByRole("article")).toHaveCount(1);
    await page.getByRole("link", { name: /Authorization boundary/ }).click();
    await expect(page.getByRole("heading", { name: "Authorization boundary" })).toBeVisible();
    await expect(page.getByText("missing_required_links")).toBeVisible();
    await expect(page.getByRole("heading", { name: "Code changes" })).toBeVisible();
    await expect(page.getByText("Merge request")).toBeVisible();
    await expect(page.getByRole("note")).toContainText("Source binding mismatch");
    await expect(page.getByRole("link", { name: "Authorization mirror" })).toHaveAttribute("rel", "noopener noreferrer");
    const detailAccessibility = await new AxeBuilder({ page }).analyze();
    expect(detailAccessibility.violations).toEqual([]);
    await page.evaluate(() => (document.activeElement as HTMLElement | null)?.blur());
    await expect(page).toHaveScreenshot("change-detail.png", { fullPage: true, animations: "disabled" });
  }
});

test("organization and repository navigation stays inside visible context", async ({ page }, testInfo) => {
  test.skip(testInfo.project.name !== "boards-desktop-1440");
  await page.goto("/changes");
  await expect(page.getByRole("heading", { name: "See the change, not the paperwork." })).toBeVisible();
  await page.getByRole("link", { name: /Acme Studio/ }).click();
  await expect(page.getByRole("heading", { name: "Acme Studio changes" })).toBeVisible();
  await page.getByLabel("Board scope").selectOption(repoId);
  await expect(page).toHaveURL("/acme/workflow/changes");
  await expect(page.getByRole("heading", { name: "Workflow Control" })).toBeVisible();
});

function boardResponse(url: URL, source: readonly ChangeCardModel[]) {
  const stage = url.searchParams.get("stage");
  const lifecycle = url.searchParams.get("lifecycle");
  const anomaly = url.searchParams.get("anomaly");
  const filtered = source.filter((card) => (!stage || card.current_stage === stage) && (!lifecycle || card.lifecycle === lifecycle) && (!anomaly || card.anomalies.includes(anomaly)));
  const page = Number(url.searchParams.get("page") ?? "1");
  const perPage = Number(url.searchParams.get("per_page") ?? "12");
  return {
    cards: filtered.slice((page - 1) * perPage, page * perPage), page, per_page: perPage, total: filtered.length,
    counts: {
      total: filtered.length,
      active: filtered.filter((item) => item.lifecycle === "active").length,
      blocked: filtered.filter((item) => item.lifecycle === "blocked").length,
      completed: filtered.filter((item) => item.lifecycle === "completed").length,
      closed: filtered.filter((item) => item.lifecycle === "closed").length,
      proposal: filtered.filter((item) => item.current_stage === "proposal").length,
      design: filtered.filter((item) => item.current_stage === "design").length,
      implement: filtered.filter((item) => item.current_stage === "implement").length,
      unknown: filtered.filter((item) => item.current_stage === "unknown").length,
    },
    diagnostics: [{ code: "orphan_typed_artifact", count: 1 }],
  };
}

const context = {
  user: { id: "aaaaaaaa-aaaa-4aaa-8aaa-aaaaaaaaaaaa", login: "alice", display_name: "Alice", email: "alice@example.test", site_admin: true },
  credential: { kind: "session", scope_mode: "identity", repository_restricted: false },
  session: { csrf_cookie_name: "issue_spec_csrf", csrf_header_name: "X-CSRF-Token" },
  allowed_actions: ["site.admin"],
  organizations: [{ id: orgId, name: "acme", display_name: "Acme Studio", effective_permission: "admin", container_only: false, allowed_actions: ["organization.read", "organization.admin"] }],
};

const repositories = { repositories: [{ repository: { id: repoId, organization_id: orgId, name: "workflow", display_name: "Workflow Control", visibility: "private", contribution_policy: "members" }, effective_permission: "admin", allowed_actions: ["read", "contribute", "triage", "write", "repository.admin"] }] };

const progress = (total: number, completed: number, inProgress: number, blocked: number, pending: number) => ({ total, completed, in_progress: inProgress, blocked, pending });
const artifact = (id: string, number: number, title: string, state = "open", valid = true, marker = "1"): Artifact => ({ id, number, title, state, url: `/issues/${orgId}/${repoId}/${number}`, marker_version: marker, updated_at: "2026-07-11T02:00:00Z", valid });
const relationship = (provider: string, externalId: string, title: string, sourceBindingMatch: CodeChangeRelationship["source_binding_match"] = "matched"): CodeChangeRelationship => ({ provider_key: provider, relation_kind: "code_change", external_repository_id: provider === "aone" ? "Ingress/workflow" : "higress-group/issue-spec", external_id: externalId, canonical_url: `https://code.example/${provider}/changes/${externalId}`, title, lifecycle_state: "active", source_binding_match: sourceBindingMatch });

const cards: ChangeCardModel[] = [
  {
    repository: { id: repoId, name: "workflow", display_name: "Workflow Control" }, change_key: "runner-release", title: "Runner-native delivery loop", current_stage: "implement", lifecycle: "active",
    artifacts: { proposal: artifact("11111111-1111-4111-8111-111111111111", 160, "Runner proposal", "closed"), design: artifact("22222222-2222-4222-8222-222222222222", 161, "Runner design", "closed"), implement: artifact("33333333-3333-4333-8333-333333333333", 162, "Runner implementation") },
    tasks: progress(12, 8, 2, 0, 2), processes: progress(8, 5, 2, 0, 1), code_changes: [relationship("github", "163", "Server projection"), relationship("aone", "28044814", "Internal delivery")], anomalies: [], updated_at: "2026-07-11T05:00:00Z",
  },
  {
    repository: { id: repoId, name: "workflow", display_name: "Workflow Control" }, change_key: "authorization-boundary", title: "Authorization boundary", current_stage: "design", lifecycle: "blocked",
    artifacts: { proposal: artifact("44444444-4444-4444-8444-444444444444", 170, "Authority proposal", "closed"), design: artifact("55555555-5555-4555-8555-555555555555", 171, "Authority design") },
    tasks: progress(7, 3, 1, 2, 1), processes: progress(4, 1, 1, 1, 1), code_changes: [relationship("aone", "73", "Authorization mirror", "mismatched")], anomalies: ["missing_required_links", "implement_missing_predecessor", "code_change_binding_mismatch"], updated_at: "2026-07-11T04:00:00Z",
  },
  {
    repository: { id: repoId, name: "workflow", display_name: "Workflow Control" }, change_key: "durable-archive", title: "Durable specification archive", current_stage: "implement", lifecycle: "completed",
    artifacts: { proposal: artifact("66666666-6666-4666-8666-666666666666", 180, "Archive proposal", "closed"), design: artifact("77777777-7777-4777-8777-777777777777", 181, "Archive design", "closed"), implement: artifact("88888888-8888-4888-8888-888888888888", 182, "Archive implementation", "closed") },
    tasks: progress(6, 6, 0, 0, 0), processes: progress(5, 5, 0, 0, 0), code_changes: [relationship("github", "182", "Archive delivery")], anomalies: [], updated_at: "2026-07-11T03:00:00Z",
  },
  {
    repository: { id: repoId, name: "workflow", display_name: "Workflow Control" }, change_key: "marker-recovery", title: "Marker compatibility recovery", current_stage: "proposal", lifecycle: "active",
    artifacts: { proposal: artifact("99999999-9999-4999-8999-999999999999", 190, "Marker proposal", "open", false, "2") },
    tasks: progress(2, 0, 1, 0, 1), processes: progress(1, 0, 0, 0, 1), code_changes: [], anomalies: ["unsupported_marker_version", "marker_label_mismatch"], updated_at: "2026-07-11T02:00:00Z",
  },
];
