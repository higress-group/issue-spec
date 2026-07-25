import AxeBuilder from "@axe-core/playwright";
import { expect, test, type Page } from "@playwright/test";
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

- Status: \`completed\`
- Phase: \`completed\`
- Public session: \`s_demo_42\`

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
const previewCSP = "default-src 'none'; base-uri 'none'; object-src 'none'; frame-ancestors 'self'; form-action 'none'; script-src 'unsafe-inline'; style-src 'unsafe-inline'; img-src data: blob:; font-src data:; connect-src 'none'; media-src 'none'; frame-src 'none'; worker-src 'none'";
const previewDocument = `<!doctype html>
<html lang="en"><head><meta charset="utf-8"><title>Review lab</title><style>body{font:16px system-ui;padding:16px}button{margin:4px;padding:8px}.result{font-weight:700}</style></head>
<body>
  <p id="mount-count">0</p><p id="capability">waiting</p>
  <button id="increment">Increment</button><output id="count">0</output>
  <button id="single">Single answer</button><button id="multiple">Multiple answer</button>
  <button id="custom">Custom answer</button><button id="second">Second answer</button>
  <form id="blocked-form" action="/login"><button type="submit">Blocked form</button></form>
  <a id="blocked-navigation" href="/login" target="_top">Blocked navigation</a>
  <div id="parent-access" class="result"></div><div id="cookie-access" class="result"></div>
  <div id="local-storage" class="result"></div><div id="session-storage" class="result"></div>
  <div id="network-access" class="result"></div><div id="popup-access" class="result"></div>
  <div id="worker-access" class="result"></div><div id="image-access" class="result"></div>
  <script>
    var nonce = "";
    var mounts = 0;
    function result(id, blocked) { document.getElementById(id).textContent = blocked ? "blocked" : "available"; }
    window.addEventListener("message", function(event) {
      var value = event.data || {};
      if (event.source !== parent || value.type !== "issue-spec-preview-init" || value.version !== 1) return;
      nonce = value.nonce;
      mounts += 1;
      document.getElementById("mount-count").textContent = String(mounts);
      document.getElementById("capability").textContent = value.interactive_question_answers ? "answers-enabled" : "answers-disabled";
    });
    document.getElementById("increment").addEventListener("click", function() {
      var output = document.getElementById("count");
      output.value = String(Number(output.value) + 1);
    });
    function answer(question, mode, optionIds, custom) {
      parent.postMessage({version:1, nonce:nonce, question_id:question, mode:mode, option_ids:optionIds, custom:custom}, "*");
    }
    document.getElementById("single").onclick = function() { answer("QUESTION-007", "single", ["safe"], ""); };
    document.getElementById("multiple").onclick = function() { answer("QUESTION-008", "multiple", ["safe", "fast"], ""); };
    document.getElementById("custom").onclick = function() { answer("QUESTION-007", "single", [], "Use a staged rollout."); };
    document.getElementById("second").onclick = function() { answer("QUESTION-007", "single", ["fast"], ""); };
    try { void parent.document.body; result("parent-access", false); } catch (_) { result("parent-access", true); }
    try { document.cookie = "preview=1"; result("cookie-access", document.cookie === ""); } catch (_) { result("cookie-access", true); }
    try { localStorage.setItem("x", "1"); result("local-storage", false); } catch (_) { result("local-storage", true); }
    try { sessionStorage.setItem("x", "1"); result("session-storage", false); } catch (_) { result("session-storage", true); }
    fetch("/api/v1/meta").then(function(){ result("network-access", false); }).catch(function(){ result("network-access", true); });
    try { result("popup-access", window.open("about:blank") === null); } catch (_) { result("popup-access", true); }
    try {
      var worker = new Worker(URL.createObjectURL(new Blob(["postMessage('started')"], {type:"text/javascript"})));
      worker.onmessage = function(){ result("worker-access", false); worker.terminate(); };
      worker.onerror = function(){ result("worker-access", true); worker.terminate(); };
    } catch (_) { result("worker-access", true); }
    var image = new Image(); image.onerror = function(){ result("image-access", true); }; image.onload = function(){ result("image-access", false); }; image.src = "https://example.invalid/authority.png";
  </script>
</body></html>`;

let comments = [commentFixture(9, "The runner should preserve **raw Markdown** and agent metadata.")];
let previewRequests = 0;
let answerSubmissions: Array<{ question_id: string; question_digest: string; option_ids: string[]; custom: string }> = [];

function questionFixture(id: "QUESTION-007" | "QUESTION-008") {
  const multiple = id === "QUESTION-008";
  return {
    id,
    question: multiple ? "Which rollout controls should be combined?" : "Which release posture should we use?",
    blocking: true,
    default_assumption: "Use Safe.",
    issue_url: "https://example.test/acme/workflow/issues/41",
    source_url: `https://example.test/acme/workflow/issues/41#issuecomment-${multiple ? 21 : 20}`,
    choice_model: {
      version: 1,
      mode: multiple ? "multiple" : "single",
      options: [
        { id: "safe", label: "Safe", description: "Conservative", tradeoff: "Slower" },
        { id: "fast", label: "Fast", description: "Rapid", tradeoff: "Riskier" },
      ],
      allow_custom: !multiple,
    },
  };
}

const documentationQuestion = {
  id: "QUESTION-009",
  question: documentationText(
    "Which release posture should the first compact-export rollout use?",
    "compact-export 首次发布应采用哪种发布姿态？",
  ),
  blocking: true,
  default_assumption: documentationText("Use the staged rollout.", "默认采用分阶段灰度。"),
  issue_url: "https://example.test/acme/workflow/issues/41",
  source_url: "https://example.test/acme/workflow/issues/41#issuecomment-20",
  choice_model: {
    version: 1,
    mode: "single",
    options: [
      { id: "staged", label: documentationText("Staged rollout", "分阶段灰度"), description: documentationText("Enable per organization first", "先按组织逐步开启"), tradeoff: documentationText("Slower feedback", "反馈更慢") },
      { id: "fast", label: documentationText("Fast release", "快速全量"), description: documentationText("Ship to every tenant at once", "一次性发布到所有租户"), tradeoff: documentationText("Riskier rollback", "回滚风险更高") },
    ],
    allow_custom: true,
  },
};

const documentationPreviewDocument = `<!doctype html>
<html lang="${documentationText("en", "zh-CN")}"><head><meta charset="utf-8"><title>${documentationText("Design brief", "设计评审简报")}</title><style>
body{font:15px/1.6 system-ui;margin:0;padding:24px;color:#1f2933;background:#f8fafc}
h1{font-size:20px;margin:0 0 4px}
p.lead{margin:0 0 16px;color:#52606d}
.grid{display:grid;grid-template-columns:1fr 1fr;gap:12px;margin-bottom:16px}
.card{background:#fff;border:1px solid #d9e2ec;border-radius:10px;padding:14px}
.card h2{font-size:13px;letter-spacing:.04em;margin:0 0 8px;color:#486581}
.card ul{margin:0;padding-left:18px}
.pill{display:inline-block;padding:2px 10px;border-radius:999px;font-size:12px;font-weight:600}
.pill.settled{background:#e3f9e5;color:#207227}
.pill.open{background:#fff3c4;color:#8d6708}
table{width:100%;border-collapse:collapse;background:#fff;border:1px solid #d9e2ec}
th,td{padding:8px 12px;text-align:left;border-top:1px solid #e4ebf1;font-size:14px}
th{border-top:none;color:#486581;font-size:12px}
</style></head><body>
<h1>${documentationText("Design review: compact export", "设计评审：紧凑导出")}</h1>
<p class="lead">${documentationText("What changes, why it is safe, and the one decision still open.", "改了什么、为什么安全，以及尚待拍板的一个决策。")}</p>
<div class="grid">
<div class="card"><h2>${documentationText("Settled", "已确认")} <span class="pill settled">${documentationText("confirmed", "confirmed")}</span></h2><ul><li>${documentationText("Exports never include credentials.", "导出永不包含凭据。")}</li><li>${documentationText("The field allowlist lives in one schema module.", "字段白名单集中在一个 schema 模块。")}</li></ul></div>
<div class="card"><h2>${documentationText("Needs a decision", "待决策")} <span class="pill open">${documentationText("open", "open")}</span></h2><ul><li>${documentationText("Release posture: staged rollout or fast release.", "发布姿态：分阶段灰度还是快速全量。")}</li><li>${documentationText("Rollback drill owner for the first tenant wave.", "首批租户的回滚演练归属。")}</li></ul></div>
</div>
<table><tr><th>${documentationText("Task", "任务")}</th><th>${documentationText("Owner agent", "负责智能体")}</th><th>${documentationText("State", "状态")}</th></tr>
<tr><td>${documentationText("Schema allowlist", "Schema 白名单")}</td><td>worker-1</td><td>${documentationText("ready", "就绪")}</td></tr>
<tr><td>${documentationText("Export endpoint", "导出接口")}</td><td>worker-2</td><td>${documentationText("blocked by decision", "等待决策")}</td></tr>
<tr><td>${documentationText("Review and tests", "评审与测试")}</td><td>reviewer</td><td>${documentationText("queued", "排队中")}</td></tr></table>
</body></html>`;

async function stabilizeScreenshotDates(page: Page) {
  await page.locator("time[datetime]").evaluateAll((elements) => {
    const locale = document.documentElement.lang || "en";
    for (const element of elements) {
      const value = element.getAttribute("datetime");
      if (!value) continue;
      element.textContent = new Intl.DateTimeFormat(locale, {
        year: "numeric",
        month: "short",
        day: "numeric",
      }).format(new Date(value));
      element.style.display = "inline";
      element.parentElement?.style.setProperty("display", "inline");
    }
    if (locale.startsWith("en")) {
      const issueTime = document.querySelector(".detail-state-row .precise-relative-time");
      const summary = issueTime?.parentElement;
      const prefix = summary ? [...summary.childNodes].find((node) => node.nodeType === Node.TEXT_NODE) : null;
      if (prefix?.textContent?.endsWith("opened this ")) prefix.textContent += "on ";
    }
  });
}

test.beforeEach(async ({ page }) => {
  test.setTimeout(60_000);
  await installDocumentationLanguage(page);
  activeIssue = issue;
  externalContributor = false;
  previewRequests = 0;
  answerSubmissions = [];
  comments = [commentFixture(9, documentationText("The runner should preserve **raw Markdown** and agent metadata.", "运行器应保留**原始 Markdown**和智能体元数据。"))];
  await page.route("**/*", async (route) => {
    const request = route.request();
    const url = new URL(request.url());
    if (url.pathname === "/api/v1/meta") return route.fulfill({ json: { api_version: "v1", features: { bootstrap: true, personal_access_tokens: true, organizations: true, source_bindings: false, webhooks: false, change_boards: false, runner: true, recovery_exchange: true, email_notifications: true, repository_email_subscriptions: true, html_preview_execution: true, interactive_question_answers: true } } });
    if (url.pathname === "/api/v1/context") return route.fulfill({ json: { user: { id: "aaaaaaaa-aaaa-4aaa-8aaa-aaaaaaaaaaaa", login: "alice", display_name: "Alice", email: "alice@example.test", site_admin: !externalContributor }, credential: { kind: "session", scope_mode: "identity", repository_restricted: false }, session: { csrf_cookie_name: "issue_spec_csrf", csrf_header_name: "X-CSRF-Token" }, allowed_actions: externalContributor ? [] : ["site.admin"], organizations: [{ id: organizationId, name: "acme", display_name: "Acme Studio", effective_permission: externalContributor ? "read" : "admin", container_only: false, allowed_actions: externalContributor ? ["organization.read"] : ["organization.read", "organization.admin"] }] } });
    if (url.pathname === "/api/v1/profile") return route.fulfill({ json: {
      id: 1, login: "alice", display_name: "Alice", identity_display_name: "Alice",
      nickname: null, representation_version: 1, avatar_url: "", html_url: "/users/alice",
      type: "User", site_admin: !externalContributor, onboarding_completed: true,
    } });
    if (url.pathname === "/api/v1/profile/email") return route.fulfill({ json: { available: true, notification_email: "alice@example.test" } });
    if (url.pathname === `/api/v1/orgs/${organizationId}/repos/${repositoryId}/subscription`) return route.fulfill({ json: { subscribed: false, ignored: false, reason: "", representation_version: 0, collection_version: 1 } });
    if (url.pathname === `/api/v1/context/orgs/${organizationId}/repos`) return route.fulfill({ json: { repositories: [{ repository: { id: repositoryId, organization_id: organizationId, name: "workflow", display_name: "Workflow control", visibility: externalContributor ? "public" : "private", contribution_policy: externalContributor ? "public" : "members" }, effective_permission: externalContributor ? "read" : "admin", allowed_actions: externalContributor ? ["read", "contribute"] : ["read", "contribute", "triage", "write", "repository.admin"] }] } });
    if (url.pathname.toLowerCase() === "/api/v1/context/repos/acme/workflow") return route.fulfill({ json: { organization: { id: organizationId, name: "acme", display_name: "Acme Studio", effective_permission: externalContributor ? "read" : "admin", container_only: false, allowed_actions: externalContributor ? ["organization.read"] : ["organization.read", "organization.admin"] }, repository: { repository: { id: repositoryId, organization_id: organizationId, name: "workflow", display_name: "Workflow control", visibility: "public", contribution_policy: externalContributor ? "public" : "authenticated" }, effective_permission: externalContributor ? "read" : "admin", allowed_actions: externalContributor ? ["read", "contribute"] : ["read", "contribute", "triage", "write", "repository.admin"] }, authenticated: true } });
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
    if (url.pathname === "/api/v1/context/repos/acme/workflow/issues/41/relationships") return route.fulfill({ json: { relationships: externalContributor ? [] : relationships } });
    if (/^\/api\/v1\/repos\/acme\/workflow\/issues\/41\/previews\/(?:review-lab|comment-lab|issue-first|anchored-first|design-brief)$/.test(url.pathname)) {
      previewRequests += 1;
      const commentPreview = url.pathname.endsWith("/comment-lab") || url.pathname.endsWith("/anchored-first");
      expect(url.searchParams.get("source")).toBe(commentPreview ? "comment" : "issue");
      expect(url.searchParams.get("comment_id")).toBe(commentPreview ? "9" : null);
      expect(url.searchParams.get("digest")).toMatch(/^[0-9a-f]{64}$/);
      const document = url.pathname.endsWith("/anchored-first") ? "<!doctype html><p>comment</p>" : url.pathname.endsWith("/design-brief") ? documentationPreviewDocument : previewDocument;
      return route.fulfill({ status: 200, body: document, headers: { "Content-Type": "text/html; charset=utf-8", "Content-Security-Policy": previewCSP, "Cache-Control": "no-store", "Referrer-Policy": "no-referrer", "Permissions-Policy": "camera=(), microphone=(), geolocation=()" } });
    }
    if (/^\/api\/v1\/repos\/acme\/workflow\/issues\/41\/questions\/QUESTION-00[789]$/.test(url.pathname)) {
      if (url.pathname.endsWith("9")) return route.fulfill({ json: { question: documentationQuestion, representation_version: 1, body_digest: "c".repeat(64) } });
      const id = url.pathname.endsWith("8") ? "QUESTION-008" : "QUESTION-007";
      return route.fulfill({ json: { question: questionFixture(id), representation_version: 1, body_digest: id === "QUESTION-007" ? "a".repeat(64) : "b".repeat(64) } });
    }
    if (url.pathname === "/api/v1/repos/acme/workflow/issues/41/answers" && request.method() === "POST") {
      const payload = request.postDataJSON() as typeof answerSubmissions[number];
      answerSubmissions.push(payload);
      const created = commentFixture(50 + answerSubmissions.length, `ANSWER timeline ${answerSubmissions.length}`);
      comments = [...comments, created];
      return route.fulfill({ status: 201, json: {
        comment: created,
        question: questionFixture(payload.question_id === "QUESTION-008" ? "QUESTION-008" : "QUESTION-007"),
        question_representation_version: 1,
        question_body_digest: payload.question_digest,
      } });
    }
    if (url.pathname === "/repos/acme/workflow/issues/41") return route.fulfill({ json: activeIssue });
    if (url.pathname === "/repos/acme/workflow/issues") return route.fulfill({ json: url.searchParams.get("labels") ? [] : [activeIssue] });
    return route.fallback();
  });
});

test("issue detail is polished, accessible and preserves raw workflow text", async ({ page }, testInfo) => {
  await page.goto("/acme/workflow/issues/41");
  await expect(page.getByRole("heading", { level: 1 }).first()).toContainText(issueTitle, { timeout: 15_000 });
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
  if (testInfo.project.name === "issues-desktop-1440") {
    const [pageBox, timelineBox] = await Promise.all([
      page.locator(".issue-detail-page").boundingBox(),
      page.locator(".timeline").boundingBox(),
    ]);
    expect(pageBox).not.toBeNull();
    expect(timelineBox).not.toBeNull();
    expect((timelineBox?.width ?? 0) / (pageBox?.width ?? 1)).toBeGreaterThan(.68);
  }
  const overflow = await page.evaluate(() => document.documentElement.scrollWidth - document.documentElement.clientWidth);
  expect(overflow).toBeLessThanOrEqual(1);
  const accessibility = await new AxeBuilder({ page }).analyze();
  expect(accessibility.violations).toEqual([]);
  await page.evaluate(() => (document.activeElement as HTMLElement | null)?.blur());
  await stabilizeScreenshotDates(page);
  await expect(page).toHaveScreenshot(documentationSnapshot("issue-detail"), { fullPage: true, animations: "disabled" });
  await page.context().grantPermissions(["clipboard-read", "clipboard-write"], { origin: "http://127.0.0.1:4173" });
  const copyLink = page.locator("#issuecomment-9").getByRole("button", { name: documentationText("Copy link", "复制链接") });
  await copyLink.click();
  await expect(page.locator("#issuecomment-9").getByRole("button", { name: documentationText("Link copied", "已复制链接") })).toBeVisible();
  await expect.poll(() => page.evaluate(() => navigator.clipboard.readText())).toBe("http://127.0.0.1:4173/acme/workflow/issues/41#issuecomment-9");
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
    await stabilizeScreenshotDates(page);
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

test("issue comments render Mermaid flow and sequence diagrams", async ({ page }, testInfo) => {
  test.skip(testInfo.project.name !== "issues-desktop-1440");
  comments = [commentFixture(9, `The cleanup lifecycle uses two background actions.

\`\`\`mermaid
flowchart LR
  A["Task builder<br/>DB only"] --> B["Cleanup task"]
  B --> C["Worker"]
\`\`\`

\`\`\`mermaid
sequenceDiagram
  participant U as User
  participant G as Gateway
  participant W as Worker
  U->>G: DeleteGateway
  G-->>W: Schedule cleanup
\`\`\``)];

  await page.goto("/acme/workflow/issues/41#issuecomment-9");
  const diagrams = page.getByRole("img", { name: documentationText("Mermaid diagram", "Mermaid 图表") });
  await expect(diagrams).toHaveCount(2);
  await expect(diagrams.nth(0)).toHaveAttribute("src", /^data:image\/svg\+xml;charset=utf-8,/);
  await expect(diagrams.nth(1)).toHaveAttribute("src", /^data:image\/svg\+xml;charset=utf-8,/);
  const flowchartSource = decodeURIComponent((await diagrams.nth(0).getAttribute("src"))?.split(",", 2)[1] ?? "");
  expect(flowchartSource).toContain("Task");
  expect(flowchartSource).toContain("builder");
  expect(flowchartSource).toContain("DB");
  expect(flowchartSource).toContain("only");
  expect(flowchartSource).not.toContain("<foreignObject");
  expect(await diagrams.nth(0).evaluate((image: HTMLImageElement) => image.complete && image.naturalWidth > 0)).toBe(true);
  expect(await diagrams.nth(1).evaluate((image: HTMLImageElement) => image.complete && image.naturalWidth > 0)).toBe(true);
  expect(await diagrams.nth(0).evaluate(async (image: HTMLImageElement) => {
    const source = image.src;
    await new Promise((resolve) => window.setTimeout(resolve, 1_200));
    return image.isConnected && image.src === source;
  })).toBe(true);
  await expect(page.locator("#issuecomment-9 code.language-mermaid")).toHaveCount(0);
  const overflow = await page.evaluate(() => document.documentElement.scrollWidth - document.documentElement.clientWidth);
  expect(overflow).toBeLessThanOrEqual(1);
  const accessibility = await new AxeBuilder({ page }).analyze();
  expect(accessibility.violations).toEqual([]);
});

test("sandboxed review preview renders the selected first surface, blocks authority, and appends trusted answers without flashing", async ({ page }, testInfo) => {
  activeIssue = {
    ...issue,
    body: `## Review lab

\`\`\`\`html-preview id=review-lab version=1 title="Review lab" height=900
${previewDocument}
<!-- exact preview bytes may contain a shorter fence and workflow-shaped text:
\`\`\`markdown
<!-- issue-spec:type=PROCESS id=PROCESS-999 version=1 -->
\`\`\`
-->
\`\`\`\`

\`\`\`mermaid
flowchart LR
  Review --> Confirm
\`\`\``,
  };
  comments = [commentFixture(9, `Comment review

\`\`\`html-preview id=comment-lab version=1 title="Comment lab" height=480
${previewDocument}
\`\`\``)];
  await page.goto("/acme/workflow/issues/41");
  await expect(page.getByRole("button", { name: /Review lab/ })).toHaveAttribute("aria-expanded", "true");
  await expect(page.getByRole("button", { name: /Comment lab/ })).toHaveAttribute("aria-expanded", "false");
  const iframe = page.locator('iframe[title="Review lab"]');
  await expect(iframe).toHaveCount(1);
  expect(previewRequests).toBe(1);
  const mermaid = page.getByRole("img", { name: documentationText("Mermaid diagram", "Mermaid 图表") });
  await expect(mermaid).toBeVisible();
  const mermaidSource = await mermaid.getAttribute("src");

  await expect(iframe).toHaveAttribute("height", "720");
  await expect(iframe).toHaveAttribute("sandbox", "allow-scripts");
  await expect(iframe).toHaveAttribute("referrerpolicy", "no-referrer");
  await expect(iframe).not.toHaveAttribute("srcdoc", /.+/);
  expect(previewRequests).toBe(1);
  await iframe.evaluate((element) => { element.setAttribute("data-stability-probe", "original"); });
  const frame = page.frameLocator('iframe[title="Review lab"]');
  await expect(frame.locator("#capability")).toHaveText("answers-enabled");
  await frame.getByRole("button", { name: "Increment" }).click();
  await expect(frame.locator("#count")).toHaveText("1");
  for (const id of ["parent-access", "cookie-access", "local-storage", "session-storage", "network-access", "popup-access", "worker-access", "image-access"]) {
    await expect(frame.locator(`#${id}`)).toHaveText("blocked");
  }
  const pageURL = page.url();
  await frame.getByRole("button", { name: "Blocked form" }).click();
  await frame.getByRole("link", { name: "Blocked navigation" }).click();
  await expect(page).toHaveURL(pageURL);
  await expect(frame.locator("#capability")).toHaveText("answers-enabled");

  const iframeBox = await iframe.boundingBox();
  expect(iframeBox).not.toBeNull();
  expect((iframeBox?.x ?? 0) + (iframeBox?.width ?? 0)).toBeLessThanOrEqual((page.viewportSize()?.width ?? 0) + 1);
  expect(await page.evaluate(() => document.documentElement.scrollWidth - document.documentElement.clientWidth)).toBeLessThanOrEqual(1);
  expect((await mermaid.getAttribute("src"))).toBe(mermaidSource);
  const accessibility = await new AxeBuilder({ page }).analyze();
  expect(accessibility.violations).toEqual([]);

  const commentDisclosure = page.getByRole("button", { name: /Comment lab/ });
  await commentDisclosure.click();
  await page.getByRole("button", { name: documentationText("Run", "运行") }).click();
  const commentIframe = page.locator('iframe[title="Comment lab"]');
  const commentFrame = page.frameLocator('iframe[title="Comment lab"]');
  await expect(commentIframe).toHaveCount(1);
  await commentIframe.evaluate((element) => { element.setAttribute("data-stability-probe", "comment-original"); });
  await expect(commentFrame.locator("#capability")).toHaveText("answers-enabled");
  await commentFrame.getByRole("button", { name: "Increment" }).click();
  await expect(commentFrame.locator("#count")).toHaveText("1");
  await page.waitForTimeout(1_100);
  await expect(commentIframe).toHaveAttribute("data-stability-probe", "comment-original");
  await expect(commentDisclosure).toHaveAttribute("aria-expanded", "true");
  await expect(commentFrame.locator("#count")).toHaveText("1");
  await expect(commentFrame.locator("#mount-count")).toHaveText("1");
  expect(previewRequests).toBe(2);

  if (testInfo.project.name !== "issues-desktop-1440") return;
  const submit = async (button: string, visibleAnswer: string, timeline: number) => {
    await frame.getByRole("button", { name: button }).click();
    const confirmation = page.locator(".answer-confirmation");
    await expect(confirmation).toBeVisible();
    await expect(confirmation.getByText(visibleAnswer, { exact: true })).toBeVisible();
    await confirmation.getByRole("button", { name: "Confirm answer" }).click();
    await expect(page.getByText(`ANSWER timeline ${timeline}`)).toBeVisible();
    await expect(confirmation).toHaveCount(0);
    await expect(iframe).toHaveAttribute("data-stability-probe", "original");
    await expect(frame.locator("#mount-count")).toHaveText("1");
    await expect(mermaid).toHaveAttribute("src", mermaidSource ?? "");
  };
  await submit("Single answer", "Safe", 1);
  await submit("Multiple answer", "Safe, Fast", 2);
  await submit("Custom answer", "Use a staged rollout.", 3);
  await submit("Second answer", "Fast", 4);
  expect(answerSubmissions).toEqual([
    { question_id: "QUESTION-007", question_digest: "a".repeat(64), option_ids: ["safe"], custom: "" },
    { question_id: "QUESTION-008", question_digest: "b".repeat(64), option_ids: ["safe", "fast"], custom: "" },
    { question_id: "QUESTION-007", question_digest: "a".repeat(64), option_ids: [], custom: "Use a staged rollout." },
    { question_id: "QUESTION-007", question_digest: "a".repeat(64), option_ids: ["fast"], custom: "" },
  ]);
});

test("a comment anchor prioritizes that comment's first preview for default rendering", async ({ page }, testInfo) => {
  test.skip(testInfo.project.name !== "issues-desktop-1440");
  activeIssue = {
    ...issue,
    body: `\`\`\`html-preview id=issue-first version=1 title="Issue first"
<!doctype html><p>issue</p>
\`\`\``,
  };
  comments = [commentFixture(9, `\`\`\`html-preview id=anchored-first version=1 title="Anchored first"
<!doctype html><p>comment</p>
\`\`\``)];

  await page.goto("/acme/workflow/issues/41#issuecomment-9");
  await expect(page.getByRole("button", { name: /Issue first/ })).toHaveAttribute("aria-expanded", "false");
  await expect(page.getByRole("button", { name: /Anchored first/ })).toHaveAttribute("aria-expanded", "true");
  await expect(page.locator('iframe[title="Anchored first"]')).toHaveCount(1);
  await expect(page.locator('iframe[title="Issue first"]')).toHaveCount(0);
  await expect(page.frameLocator('iframe[title="Anchored first"]').getByText("comment", { exact: true })).toBeVisible();
  expect(previewRequests).toBe(1);
});

test("review projection and native question answering documentation screenshot", async ({ page }, testInfo) => {
  test.skip(testInfo.project.name !== "issues-desktop-1440");
  const briefTitle = documentationText("Design brief", "设计评审简报");
  activeIssue = {
    ...issue,
    title: documentationText("Design: compact-export", "Design：compact-export"),
    labels: [labels[0]],
    body: documentationText(
      `## Design: compact export\n\nThe export ships behind one schema allowlist module. The brief below is the human review surface; issue bodies and typed comments stay authoritative.`,
      `## 设计：紧凑导出\n\n导出能力收敛在一个 schema 白名单模块之后。下方简报是人类评审界面；issue 正文与类型化评论仍是权威数据。`,
    ) + `\n\n\`\`\`html-preview id=design-brief version=1 title="${briefTitle}" height=620\n${documentationPreviewDocument}\n\`\`\``,
  };
  comments = [commentFixture(20, documentationText(
    `<!-- issue-spec:type=QUESTION id=QUESTION-009 version=1 -->\nAgent: Design\nType: QUESTION\nID: QUESTION-009\nStatus: open\nScope: release posture\nLinks: SPEC-001\n\n## Question\nWhich release posture should the first compact-export rollout use?`,
    `<!-- issue-spec:type=QUESTION id=QUESTION-009 version=1 -->\nAgent: Design\nType: QUESTION\nID: QUESTION-009\nStatus: open\nScope: 发布姿态\nLinks: SPEC-001\n\n## Question\ncompact-export 首次发布应采用哪种发布姿态？`,
  ))];

  await page.goto("/acme/workflow/issues/41");
  await expect(page.getByRole("heading", { level: 1 }).first()).toContainText(activeIssue.title);
  const iframe = page.locator(`iframe[title="${briefTitle}"]`);
  await expect(iframe).toHaveCount(1);
  await expect(page.frameLocator(`iframe[title="${briefTitle}"]`).getByText(documentationText("Design review: compact export", "设计评审：紧凑导出"))).toBeVisible();
  const panel = page.getByRole("region", { name: /QUESTION-009/ });
  await expect(panel.getByRole("radio", { name: new RegExp(documentationText("Staged rollout", "分阶段灰度")) })).toBeVisible();
  await expect(panel.getByRole("radio", { name: new RegExp(documentationText("Fast release", "快速全量")) })).toBeVisible();
  await page.evaluate(() => (document.activeElement as HTMLElement | null)?.blur());
  await stabilizeScreenshotDates(page);
  await expect(page).toHaveScreenshot(documentationSnapshot("review-projection"), { fullPage: true, animations: "disabled" });
});

test("issue list presents the repository subscription entry", async ({ page }, testInfo) => {
  await page.goto("/acme/workflow/issues");
  await expect(page.getByRole("heading", { name: documentationText("Issues", "议题") })).toBeVisible();
  await expect(page.getByRole("button", { name: documentationText("Subscribe to repository", "订阅仓库") })).toHaveAttribute("aria-pressed", "false");
  const accessibility = await new AxeBuilder({ page }).analyze();
  expect(accessibility.violations).toEqual([]);
  if (testInfo.project.name === "issues-desktop-1440") {
    await page.evaluate(() => (document.activeElement as HTMLElement | null)?.blur());
    await stabilizeScreenshotDates(page);
    await expect(page).toHaveScreenshot(documentationSnapshot("issue-list"), { fullPage: true, animations: "disabled" });
  }
});

test("combined label filters produce an intentional empty state", async ({ page }, testInfo) => {
  test.skip(testInfo.project.name !== "issues-desktop-1440");
  await page.goto("/acme/workflow/issues");
  await page.locator("summary").filter({ hasText: documentationText("Labels", "标签") }).click();
  await page.getByRole("checkbox", { name: "issue-spec/design" }).click();
  await expect(page).toHaveURL(/labels=issue-spec%2Fdesign/);
  await page.locator("summary").filter({ hasText: documentationText("Labels", "标签") }).click();
  await page.getByRole("checkbox", { name: "runner" }).click();
  await expect(page).toHaveURL(/labels=issue-spec%2Fdesign%2Crunner/);
  await expect(page.getByRole("heading", { name: documentationText("No issues match this view", "没有符合当前条件的议题") })).toBeVisible();
});

test("external contributor simple request is an ordinary issue", async ({ page }, testInfo) => {
  test.skip(testInfo.project.name !== "issues-desktop-1440");
  externalContributor = true;
  comments = [];
  activeIssue = {
    ...issue,
    title: documentationText("Allow a compact export format", "支持紧凑导出格式"),
    body: documentationText(
      "Our small team needs a compact export that can be attached to a support request. A free-form issue is enough to start the discussion.",
      "我们的小团队需要一种可附在支持请求中的紧凑导出格式。先用自由格式 Issue 讨论即可。",
    ),
    labels: [],
    comments: 0,
  };
  await page.goto("/acme/workflow/issues/41");
  await expect(page.getByRole("heading", { level: 1 }).first()).toContainText(activeIssue.title);
  await expect(page.getByText("issue-spec/proposal")).toHaveCount(0);
  await page.evaluate(() => (document.activeElement as HTMLElement | null)?.blur());
  await stabilizeScreenshotDates(page);
  await expect(page).toHaveScreenshot(documentationSnapshot("requirements-simple-issue"), { fullPage: true, animations: "disabled" });
});

test("external contributor standard request keeps Proposal SPEC QUESTION and discussion together", async ({ page }, testInfo) => {
  test.skip(testInfo.project.name !== "issues-desktop-1440");
  externalContributor = true;
  activeIssue = {
    ...issue,
    title: documentationText("Proposal: compact-export", "Proposal：compact-export"),
    body: documentationText(
      "<!-- issue-spec:issue=proposal change=compact-export version=1 -->\n# Proposal: compact-export\n\n## Background\nSupport requests need a small, portable export.\n\n## Goals\n- Preserve the fields needed to reproduce a report.\n- Keep credentials out of the export.",
      "<!-- issue-spec:issue=proposal change=compact-export version=1 -->\n# Proposal：compact-export\n\n## 背景\n支持请求需要小巧、可移植的导出文件。\n\n## 目标\n- 保留复现报告所需字段。\n- 导出中不得包含凭据。",
    ),
    labels: [{ id: 3, name: "issue-spec/proposal", color: "0969da", default: false, description: "Proposal", url: "" }],
    comments: 3,
  };
  comments = [
    commentFixture(12, documentationText(
      "<!-- issue-spec:type=SPEC id=SPEC-001 version=1 -->\nAgent: Requirements\nType: SPEC\nID: SPEC-001\nStatus: confirmed\nScope: compact export\nLinks:\n\n## Requirement: credential-free export\nThe export MUST contain only the fields needed to reproduce the report.",
      "<!-- issue-spec:type=SPEC id=SPEC-001 version=1 -->\nAgent: Requirements\nType: SPEC\nID: SPEC-001\nStatus: confirmed\nScope: 紧凑导出\nLinks:\n\n## Requirement：无凭据导出\n导出必须只包含复现报告所需字段。",
    )),
    commentFixture(13, documentationText(
      "<!-- issue-spec:type=QUESTION id=QUESTION-001 version=1 -->\nAgent: Requirements\nType: QUESTION\nID: QUESTION-001\nStatus: open\nScope: export size\nLinks:\n\n## Question\nShould attachments be excluded from the first version?",
      "<!-- issue-spec:type=QUESTION id=QUESTION-001 version=1 -->\nAgent: Requirements\nType: QUESTION\nID: QUESTION-001\nStatus: open\nScope: 导出大小\nLinks:\n\n## Question\n首个版本是否应排除附件？",
    )),
    commentFixture(14, documentationText("Attachments can remain out of scope for the first version.", "首个版本可以先不包含附件。")),
  ];
  await page.goto("/acme/workflow/issues/41");
  await expect(page.getByText("ID: SPEC-001", { exact: false })).toBeVisible();
  await expect(page.getByText("ID: QUESTION-001", { exact: false })).toBeVisible();
  await page.evaluate(() => (document.activeElement as HTMLElement | null)?.blur());
  await stabilizeScreenshotDates(page);
  await expect(page).toHaveScreenshot(documentationSnapshot("requirements-standard-proposal"), { fullPage: true, animations: "disabled" });
});

test("repository roots and legacy UUID links converge on canonical named routes", async ({ page }, testInfo) => {
  test.skip(testInfo.project.name !== "issues-desktop-1440");
  await page.goto("/AcMe/WorkFlow?state=open#issue-list");
  await expect(page).toHaveURL("/AcMe/WorkFlow/issues?state=open#issue-list");
  await expect(page.getByRole("heading", { level: 1 }).first()).toBeVisible();

  await page.goto(`/issues/${organizationId}/${repositoryId}?state=open#issue-list`);
  await expect(page).toHaveURL("/acme/workflow/issues?state=open#issue-list");

  await page.goto(`/issues/${organizationId}/${repositoryId}/41?view=timeline#issuecomment-9`);
  await expect(page).toHaveURL("/acme/workflow/issues/41?view=timeline#issuecomment-9");
  await expect(page.getByRole("heading", { level: 1 }).first()).toContainText(issueTitle);
  await expect(page.locator("#issuecomment-9")).toBeFocused();
});

test("canonical public WebURL keeps its owner/repository route and comment fragment", async ({ page }, testInfo) => {
  test.skip(!["issues-desktop-1440", "issues-mobile-390"].includes(testInfo.project.name));
  comments = [
    ...Array.from({ length: 8 }, (_, index) => commentFixture(index + 1, `Earlier comment ${index + 1}\n\nKeep the permalink target below the initial viewport.`)),
    commentFixture(9, "Permalink target comment."),
    commentFixture(10, "Later comment 10 keeps enough content below the permalink target."),
    commentFixture(11, "Later comment 11 keeps enough content below the permalink target."),
  ];
  await page.goto("/AcMe/WorkFlow/issues/41?view=timeline#issuecomment-9");
  await expect(page).toHaveURL("/AcMe/WorkFlow/issues/41?view=timeline#issuecomment-9");
  await expect(page.getByRole("heading", { level: 1 }).first()).toContainText(issueTitle, { timeout: 15_000 });
  const target = page.locator("#issuecomment-9");
  const expectLockedToTarget = async () => {
    await expect(target).toBeVisible();
    await expect(target).toBeInViewport();
    await expect(target).toBeFocused();
    await expect(target).toHaveCSS("border-color", "rgb(23, 109, 103)");
    const box = await target.boundingBox();
    expect(box).not.toBeNull();
    expect(box?.y ?? -1).toBeGreaterThanOrEqual(testInfo.project.name === "issues-mobile-390" ? 68 : 0);
    expect(box?.y ?? Number.POSITIVE_INFINITY).toBeLessThan(100);
    expect(await page.evaluate(() => window.scrollY)).toBeGreaterThan(0);
  };
  await expectLockedToTarget();
  await page.reload();
  await expectLockedToTarget();
  const overflow = await page.evaluate(() => document.documentElement.scrollWidth - document.documentElement.clientWidth);
  expect(overflow).toBeLessThanOrEqual(1);
});

const labels = [{ id: 1, name: "issue-spec/design", color: "62459a", default: false, description: "Design", url: "" }, { id: 2, name: "runner", color: "0f6f6f", default: false, description: "Runner", url: "" }];
const relationships = [
  { provider_key: "github", code_change_label: "Pull request", relation_kind: "code_change", external_repository_id: "acme/workflow", external_id: "42", canonical_url: "https://code.example/acme/workflow/pull/42", title: documentationText("Runner projection", "运行器投影"), lifecycle_state: "active", source_binding_match: "matched" },
  { provider_key: "aone-bridge", code_change_label: "Merge request", relation_kind: "code_change", external_repository_id: "Ingress/workflow", external_id: "73", canonical_url: "https://code.example/Ingress/workflow/merge_requests/73", title: documentationText("Internal mirror", "内部镜像"), lifecycle_state: "active", source_binding_match: "mismatched" },
];
const issue = { id: 41, number: 41, state: "open", state_reason: null, title: issueTitle, body: rawBody, user, labels, locked: false, comments: 1, created_at: "2026-07-10T10:00:00Z", updated_at: "2026-07-10T10:00:00Z", closed_at: null, html_url: "https://code.example.test/acme/workflow/issues/41", reactions: reactionSummary };
let activeIssue = issue;
let externalContributor = false;
function commentFixture(id: number, body: string, author = user) { return { id, body, user: author, created_at: "2026-07-10T11:00:00Z", updated_at: "2026-07-10T11:00:00Z", html_url: `https://code.example.test/acme/workflow/issues/41#issuecomment-${id}`, reactions: id === 9 ? reactionSummary : { ...reactionSummary, total_count: 0, "+1": 0 } }; }
