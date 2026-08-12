import { expect, test } from "@playwright/test";
import { documentationSnapshot, documentationText, installDocumentationLanguage } from "../../../tests/e2e/documentation-language";

const organizationId = "bbbbbbbb-bbbb-4bbb-8bbb-bbbbbbbbbbbb";
const repositoryId = "cccccccc-cccc-4ccc-8ccc-cccccccccccc";
const repositoryDisplayName = "acme/workflow";
const changeKey = "compact-export";

function searchIssue(id: string, number: number, title: string, key: string, matches: Array<{ source: "issue"; excerpt: string }>) {
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
    changes: [{ key, stage: "proposal", matched: false }],
    score: 12,
    matches,
  };
}

const searchResults = {
  items: [
    searchIssue("11111111-1111-4111-8111-111111111111", 38, documentationText("Proposal: compact-export", "Proposal：compact-export"), changeKey, [
      { source: "issue", excerpt: documentationText(
        "# Proposal: compact export → every exported field passes the schema allowlist; scrubbing runs before serialization…",
        "# 提议：紧凑导出 → 每个导出字段都经过 schema 白名单；脱敏发生在序列化之前…",
      ) },
    ]),
    searchIssue("22222222-2222-4222-8222-222222222222", 39, documentationText("Proposal: export allowlist", "Proposal：export-allowlist"), "export-allowlist", [
      { source: "issue", excerpt: documentationText(
        "…the export endpoint uses the schema allowlist module and documents the shared boundary…",
        "…导出接口使用 schema 白名单模块，并记录共享边界…",
      ) },
    ]),
    {
      ...searchIssue("33333333-3333-4333-8333-333333333333", 24, documentationText("Proposal: report redaction", "Proposal：report-redaction"), "report-redaction", [
        { source: "issue", excerpt: documentationText(
          "…the earlier redaction change settled the credential-free boundary this export reuses…",
          "…早先的脱敏变更确定了本次导出复用的无凭据边界…",
        ) },
      ]),
      changes: [{ key: "report-redaction", stage: "proposal" as const, matched: false }],
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
    if (url.pathname === `/api/v1/context/orgs/${organizationId}/repos`) return route.fulfill({ json: { repositories: [{ repository: { id: repositoryId, organization_id: organizationId, name: "workflow", display_name: repositoryDisplayName, visibility: "private", contribution_policy: "members" }, effective_permission: "admin", allowed_actions: ["read", "contribute", "triage", "write", "repository.admin"] }] } });
    if (url.pathname === `/api/v1/orgs/${organizationId}/repos/${repositoryId}/search/issues`) {
      expect(url.searchParams.get("q")).toBe(documentationText("schema allowlist", "白名单"));
      return route.fulfill({ json: searchResults });
    }
    return route.fallback();
  });
});

test("repository search renders Proposal matches documentation screenshot", async ({ page }) => {
  const query = documentationText("schema allowlist", "白名单");
  await page.goto(`/search/${organizationId}/repos/${repositoryId}?q=${encodeURIComponent(query)}`);
  await expect(page.getByRole("heading", { name: repositoryDisplayName }).first()).toBeVisible();
  const relatedChange = page.getByRole("link", { name: new RegExp(changeKey) }).first();
  await expect(relatedChange).toBeVisible();
  await expect(page.getByRole("link", { name: documentationText("Proposal: compact-export", "Proposal：compact-export") })).toBeVisible();
  await expect(page.getByRole("link", { name: documentationText("Proposal: export allowlist", "Proposal：export-allowlist") })).toBeVisible();
  await page.evaluate(() => (document.activeElement as HTMLElement | null)?.blur());
  await expect(page).toHaveScreenshot(documentationSnapshot("search-related-changes"), { fullPage: true, animations: "disabled" });
});
