import AxeBuilder from "@axe-core/playwright";
import { expect, test } from "@playwright/test";
import { documentationSnapshot, documentationText, installDocumentationLanguage } from "../../../tests/e2e/documentation-language";

const organizationId = "bbbbbbbb-bbbb-4bbb-8bbb-bbbbbbbbbbbb";
const repositoryId = "cccccccc-cccc-4ccc-8ccc-cccccccccccc";
const issueTitle = documentationText("Runner contract", "运行器契约");
const rawBody = documentationText(
  `## Runner contract\n\nKeep agent sessions traceable without losing the original workflow source.\n\n<!-- issue-spec:type=PROCESS id=PROCESS-010 version=1 -->\n\n- [x] Compatible issue route\n- [ ] Browser validation\n\n| Surface | State |\n| --- | --- |\n| CLI | ready |\n| Web | review |\n\n\`\`\`sh\nissue-spec runner serve --repo acme/workflow\n\`\`\``,
  `## 运行器契约\n\n保留智能体会话的完整追溯关系，同时不丢失原始工作流来源。\n\n<!-- issue-spec:type=PROCESS id=PROCESS-010 version=1 -->\n\n- [x] 兼容议题路由\n- [ ] 浏览器验证\n\n| 界面 | 状态 |\n| --- | --- |\n| CLI | 就绪 |\n| Web | 评审中 |\n\n\`\`\`sh\nissue-spec runner serve --repo acme/workflow\n\`\`\``,
);
const user = { login: "alice", id: 1, avatar_url: "", html_url: "", type: "User", site_admin: false };
const runnerUser = { login: "issue-spec-runner", id: 2, avatar_url: "", html_url: "", type: "Bot", site_admin: false };
const runnerCommand = documentationText(
  "/new Update the implementation and open a pull request with tests.",
  "/new 更新实现并提交一个包含测试的拉取请求。",
);
const runnerStatus = `<!-- issue-spec-runner:status {"schema_version":1,"status_writeback_key":"status:fixture"} -->
### issue-spec runner status

| Field | Value |
| --- | --- |
| Status | \`completed\` |
| Phase | \`completed\` |
| Public session | \`s_demo_42\` |

## Result

- Completed the requested command.
- Updated PROCESS-042 with implementation evidence.

## Continue Session

To continue this session, create a new command comment:

\`\`\`text
/resume s_demo_42 <answer or next instruction>
\`\`\`

Ordinary follow-up comments remain visible but are ignored by runner intake.`;
const reactionSummary = { total_count: 1, "+1": 1, "-1": 0, laugh: 0, hooray: 0, confused: 0, heart: 0, rocket: 0, eyes: 0, url: "" };

let comments = [commentFixture(9, "The runner should preserve **raw Markdown** and agent metadata.")];

test.beforeEach(async ({ page }) => {
  await installDocumentationLanguage(page);
  comments = [commentFixture(9, documentationText("The runner should preserve **raw Markdown** and agent metadata.", "运行器应保留**原始 Markdown**和智能体元数据。"))];
  await page.route("**/*", async (route) => {
    const request = route.request();
    const url = new URL(request.url());
    if (url.pathname === "/api/v1/meta") return route.fulfill({ json: { api_version: "v1", features: { bootstrap: true, personal_access_tokens: true, organizations: true, source_bindings: false, webhooks: false, change_boards: false, runner: true, recovery_exchange: true } } });
    if (url.pathname === "/api/v1/context") return route.fulfill({ json: { user: { id: "aaaaaaaa-aaaa-4aaa-8aaa-aaaaaaaaaaaa", login: "alice", display_name: "Alice", email: "alice@example.test", site_admin: true }, credential: { kind: "session", scope_mode: "identity", repository_restricted: false }, session: { csrf_cookie_name: "issue_spec_csrf", csrf_header_name: "X-CSRF-Token" }, allowed_actions: ["site.admin"], organizations: [{ id: organizationId, name: "acme", display_name: "Acme Studio", effective_permission: "admin", container_only: false, allowed_actions: ["organization.read", "organization.admin"] }] } });
    if (url.pathname === `/api/v1/context/orgs/${organizationId}/repos`) return route.fulfill({ json: { repositories: [{ repository: { id: repositoryId, organization_id: organizationId, name: "workflow", display_name: "Workflow control", visibility: "private", contribution_policy: "members" }, effective_permission: "admin", allowed_actions: ["read", "contribute", "triage", "write", "repository.admin"] }] } });
    if (url.pathname.toLowerCase() === "/api/v1/context/repos/acme/workflow") return route.fulfill({ json: { organization: { id: organizationId, name: "acme", display_name: "Acme Studio", effective_permission: "admin", container_only: false, allowed_actions: ["organization.read", "organization.admin"] }, repository: { repository: { id: repositoryId, organization_id: organizationId, name: "workflow", display_name: "Workflow control", visibility: "public", contribution_policy: "authenticated" }, effective_permission: "admin", allowed_actions: ["read", "contribute", "triage", "write", "repository.admin"] }, authenticated: true } });
    if (url.pathname === "/repos/acme/workflow/labels") return route.fulfill({ json: labels });
    if (url.pathname === "/repos/acme/workflow/issues/41/comments" && request.method() === "GET") return route.fulfill({ json: comments });
    if (url.pathname === "/repos/acme/workflow/issues/41/comments" && request.method() === "POST") {
      const payload = request.postDataJSON() as { body: string };
      const created = commentFixture(Math.max(...comments.map((comment) => comment.id)) + 1, payload.body);
      comments = [...comments, created];
      return route.fulfill({ status: 201, json: created });
    }
    if (url.pathname === "/repos/acme/workflow/issues/comments/9/reactions") return route.fulfill({ json: [{ id: 7, user: user, content: "+1", created_at: "2026-07-10T12:00:00Z" }] });
    if (/^\/repos\/acme\/workflow\/issues\/comments\/\d+\/reactions$/.test(url.pathname)) return route.fulfill({ json: [] });
    if (url.pathname === "/api/v1/context/repos/acme/workflow/issues/41/relationships") return route.fulfill({ json: { relationships } });
    if (url.pathname === "/repos/acme/workflow/issues/41") return route.fulfill({ json: issue });
    if (url.pathname === "/repos/acme/workflow/issues") return route.fulfill({ json: url.searchParams.get("labels") ? [] : [issue] });
    return route.fallback();
  });
});

test("issue detail is polished, accessible and preserves raw workflow text", async ({ page }, testInfo) => {
  await page.goto(`/issues/${organizationId}/${repositoryId}/41`);
  await expect(page.getByRole("heading", { level: 1 }).first()).toContainText(issueTitle);
  if (testInfo.project.name === "issues-mobile-390") {
    const backLink = page.locator(".detail-title .issue-back");
    const title = page.locator(".detail-title h1");
    const [backBox, titleBox] = await Promise.all([backLink.boundingBox(), title.boundingBox()]);
    expect(backBox).not.toBeNull();
    expect(titleBox).not.toBeNull();
    expect((backBox?.y ?? 0) + (backBox?.height ?? 0)).toBeLessThanOrEqual((titleBox?.y ?? 0) + 1);
  }
  await expect(page.getByTestId("rendered-markdown").first()).not.toContainText("issue-spec:type");
  await expect(page.getByText(documentationText("Pull request", "拉取请求"))).toBeVisible();
  await expect(page.getByRole("link", { name: documentationText("Runner projection", "运行器投影") })).toHaveAttribute("href", "https://code.example/acme/workflow/pull/42");
  await expect(page.getByText(documentationText("Binding mismatch", "绑定不一致"))).toBeVisible();
  const overflow = await page.evaluate(() => document.documentElement.scrollWidth - document.documentElement.clientWidth);
  expect(overflow).toBeLessThanOrEqual(1);
  const accessibility = await new AxeBuilder({ page }).analyze();
  expect(accessibility.violations).toEqual([]);
  await page.evaluate(() => (document.activeElement as HTMLElement | null)?.blur());
  await expect(page).toHaveScreenshot(documentationSnapshot("issue-detail"), { fullPage: true, animations: "disabled" });
  if (testInfo.project.name === "issues-desktop-1440") {
    comments = [
      ...comments,
      commentFixture(10, runnerCommand),
      commentFixture(11, runnerStatus, runnerUser),
    ];
    await page.reload();
    await expect(page.getByText(runnerCommand)).toBeVisible();
    await expect(page.getByText("/resume s_demo_42 <answer or next instruction>")).toBeVisible();
    await page.evaluate(() => (document.activeElement as HTMLElement | null)?.blur());
    await expect(page).toHaveScreenshot(documentationSnapshot("runner-command"), { fullPage: true, animations: "disabled" });

    await page.getByRole("button", { name: documentationText("Edit", "编辑") }).first().click();
    await expect(page.getByRole("textbox", { name: documentationText("Description", "描述") })).toHaveValue(rawBody);
    await page.getByRole("button", { name: documentationText("Cancel", "取消"), exact: true }).click();
    const comment = page.getByRole("textbox", { name: documentationText("Comment", "评论") });
    const newComment = documentationText("A fresh browser decision", "一条新的浏览器端决定");
    await comment.fill(newComment);
    await page.getByRole("button", { name: documentationText("Comment", "评论"), exact: true }).click();
    await expect(comment).toHaveValue("");
    await expect(page.getByText(newComment)).toBeVisible();
  }
});

test("combined label filters produce an intentional empty state", async ({ page }, testInfo) => {
  test.skip(testInfo.project.name !== "issues-desktop-1440");
  await page.goto(`/issues/${organizationId}/${repositoryId}`);
  await page.locator("summary").filter({ hasText: documentationText("Labels", "标签") }).click();
  await page.getByRole("checkbox", { name: "issue-spec/design" }).click();
  await expect(page).toHaveURL(/labels=issue-spec%2Fdesign/);
  await page.locator("summary").filter({ hasText: documentationText("Labels", "标签") }).click();
  await page.getByRole("checkbox", { name: "runner" }).click();
  await expect(page).toHaveURL(/labels=issue-spec%2Fdesign%2Crunner/);
  await expect(page.getByRole("heading", { name: documentationText("No issues match this view", "没有符合当前条件的议题") })).toBeVisible();
});

test("canonical public WebURL keeps its owner/repository route and comment fragment", async ({ page }, testInfo) => {
  test.skip(!["issues-desktop-1440", "issues-mobile-390"].includes(testInfo.project.name));
  await page.goto("/AcMe/WorkFlow/issues/41?view=timeline#issuecomment-9");
  await expect(page).toHaveURL("/AcMe/WorkFlow/issues/41?view=timeline#issuecomment-9");
  await expect(page.getByRole("heading", { level: 1 }).first()).toContainText(issueTitle);
  await expect(page.locator("#issuecomment-9")).toBeVisible();
  const overflow = await page.evaluate(() => document.documentElement.scrollWidth - document.documentElement.clientWidth);
  expect(overflow).toBeLessThanOrEqual(1);
});

const labels = [{ id: 1, name: "issue-spec/design", color: "62459a", default: false, description: "Design", url: "" }, { id: 2, name: "runner", color: "0f6f6f", default: false, description: "Runner", url: "" }];
const relationships = [
  { provider_key: "github", code_change_label: "Pull request", relation_kind: "code_change", external_repository_id: "acme/workflow", external_id: "42", canonical_url: "https://code.example/acme/workflow/pull/42", title: documentationText("Runner projection", "运行器投影"), lifecycle_state: "active", source_binding_match: "matched" },
  { provider_key: "aone-bridge", code_change_label: "Merge request", relation_kind: "code_change", external_repository_id: "Ingress/workflow", external_id: "73", canonical_url: "https://code.example/Ingress/workflow/merge_requests/73", title: documentationText("Internal mirror", "内部镜像"), lifecycle_state: "active", source_binding_match: "mismatched" },
];
const issue = { id: 41, number: 41, state: "open", state_reason: null, title: issueTitle, body: rawBody, user, labels, locked: false, comments: 1, created_at: "2026-07-10T10:00:00Z", updated_at: "2026-07-10T10:00:00Z", closed_at: null, html_url: "https://code.example.test/acme/workflow/issues/41", reactions: reactionSummary };
function commentFixture(id: number, body: string, author = user) { return { id, body, user: author, created_at: "2026-07-10T11:00:00Z", updated_at: "2026-07-10T11:00:00Z", html_url: `https://code.example.test/acme/workflow/issues/41#issuecomment-${id}`, reactions: id === 9 ? reactionSummary : { ...reactionSummary, total_count: 0, "+1": 0 } }; }
