import { expect, test } from "@playwright/test";
import { documentationSnapshot, documentationText, installDocumentationLanguage } from "../../../tests/e2e/documentation-language";

const organizationId = "bbbbbbbb-bbbb-4bbb-8bbb-bbbbbbbbbbbb";
const repositoryId = "cccccccc-cccc-4ccc-8ccc-cccccccccccc";
const changeKey = "compact-export";

function searchIssue(id: string, number: number, title: string, stage: "proposal" | "design" | "implement", matches: Array<{ source: "issue" | "comment"; excerpt: string }>) {
  return {
    organization_id: organizationId,
    organization: "acme",
    repository_id: repositoryId,
    repository: "workflow",
    id,
    number,
    title,
    state: "open",
    updated_at: "2026-07-22T09:30:00+08:00",
    url: `https://example.test/acme/workflow/issues/${number}`,
    changes: [{ key: changeKey, stage, matched: true }],
    score: 12,
    matches,
  };
}

const searchResults = {
  items: [
    searchIssue("11111111-1111-4111-8111-111111111111", 38, documentationText("Design: compact-export", "Design：compact-export"), "design", [
      { source: "issue", excerpt: documentationText(
        "# Design: compact export → every exported field passes the schema allowlist; scrubbing runs before serialization…",
        "# 设计：紧凑导出 → 每个导出字段都经过 schema 白名单；脱敏发生在序列化之前…",
      ) },
      { source: "comment", excerpt: documentationText(
        "…TASK-002 · wire the scrubber into the export pipeline and keep allowlist round-trip tests green…",
        "…TASK-002 · 将脱敏器接入导出流水线，并保持白名单双向测试通过…",
      ) },
    ]),
    searchIssue("22222222-2222-4222-8222-222222222222", 39, documentationText("Implement: compact-export", "Implement：compact-export"), "implement", [
      { source: "comment", excerpt: documentationText(
        "…PROCESS-002 · export endpoint waits on the schema allowlist module; shared touchpoint documented…",
        "…PROCESS-002 · 导出接口等待 schema 白名单模块；共享触点已记录…",
      ) },
    ]),
    {
      ...searchIssue("33333333-3333-4333-8333-333333333333", 24, documentationText("Proposal: report redaction", "Proposal：report-redaction"), "proposal", [
        { source: "issue", excerpt: documentationText(
          "…the earlier redaction change settled the credential-free boundary this export reuses…",
          "…早先的脱敏变更确定了本次导出复用的无凭据边界…",
        ) },
      ]),
      changes: [{ key: "report-redaction", stage: "proposal" as const, matched: true }],
    },
  ],
  page: 1,
  per_page: 12,
  total: 3,
  has_next: false,
};

test.beforeEach(async ({ page }) => {
  await installDocumentationLanguage(page);
  await page.route("**/*", async (route) => {
    const request = route.request();
    const url = new URL(request.url());
    if (url.pathname === "/api/v1/meta") return route.fulfill({ json: { api_version: "v1", features: { bootstrap: true, personal_access_tokens: true, organizations: true, source_bindings: false, webhooks: false, change_boards: true, runner: true, recovery_exchange: true, search: true } } });
    if (url.pathname === "/api/v1/context") return route.fulfill({ json: { user: { id: "aaaaaaaa-aaaa-4aaa-8aaa-aaaaaaaaaaaa", login: "alice", display_name: "Alice", email: "alice@example.test", site_admin: true }, credential: { kind: "session", scope_mode: "identity", repository_restricted: false }, session: { csrf_cookie_name: "issue_spec_csrf", csrf_header_name: "X-CSRF-Token" }, allowed_actions: ["site.admin"], organizations: [{ id: organizationId, name: "acme", display_name: "Acme Studio", effective_permission: "admin", container_only: false, allowed_actions: ["organization.read", "organization.admin"] }] } });
    if (url.pathname === `/api/v1/context/orgs/${organizationId}/repos`) return route.fulfill({ json: { repositories: [{ repository: { id: repositoryId, organization_id: organizationId, name: "workflow", display_name: "Workflow control", visibility: "private", contribution_policy: "members" }, effective_permission: "admin", allowed_actions: ["read", "contribute", "triage", "write", "repository.admin"] }] } });
    if (url.pathname === `/api/v1/orgs/${organizationId}/repos/${repositoryId}/search/issues`) {
      expect(url.searchParams.get("q")).toBe(documentationText("schema allowlist", "白名单"));
      return route.fulfill({ json: searchResults });
    }
    return route.fallback();
  });
});

test("repository search groups matches by related change documentation screenshot", async ({ page }) => {
  const query = documentationText("schema allowlist", "白名单");
  await page.goto(`/search/${organizationId}/repos/${repositoryId}?q=${encodeURIComponent(query)}`);
  await expect(page.getByRole("heading", { name: documentationText("Workflow control", "Workflow control") }).first()).toBeVisible();
  const relatedChange = page.getByRole("link", { name: new RegExp(changeKey) }).first();
  await expect(relatedChange).toBeVisible();
  await expect(page.getByRole("link", { name: documentationText("Design: compact-export", "Design：compact-export") })).toBeVisible();
  await expect(page.getByRole("link", { name: documentationText("Implement: compact-export", "Implement：compact-export") })).toBeVisible();
  await page.evaluate(() => (document.activeElement as HTMLElement | null)?.blur());
  await expect(page).toHaveScreenshot(documentationSnapshot("search-related-changes"), { fullPage: true, animations: "disabled" });
});
