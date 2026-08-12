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
  <p id="mount-count">1</p><p id="capability">display-only</p>
  <button id="increment">Increment</button><output id="count">0</output>
  <form id="blocked-form" action="/login"><button type="submit">Blocked form</button></form>
  <a id="blocked-navigation" href="/login" target="_top">Blocked navigation</a>
  <div id="parent-access" class="result"></div><div id="cookie-access" class="result"></div>
  <div id="local-storage" class="result"></div><div id="session-storage" class="result"></div>
  <div id="network-access" class="result"></div><div id="popup-access" class="result"></div>
  <div id="worker-access" class="result"></div><div id="image-access" class="result"></div>
  <script>
    function result(id, blocked) { document.getElementById(id).textContent = blocked ? "blocked" : "available"; }
    document.getElementById("increment").addEventListener("click", function() {
      var output = document.getElementById("count");
      output.value = String(Number(output.value) + 1);
    });
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
    "Should attachments be included in the first compact-export version?",
    "紧凑导出第一版是否包含附件？",
  ),
  blocking: true,
  default_assumption: documentationText("Defer attachments to a later version.", "默认推迟附件到后续版本。"),
  issue_url: "https://example.test/acme/workflow/issues/41",
  source_url: "https://example.test/acme/workflow/issues/41#issuecomment-20",
  choice_model: {
    version: 1,
    mode: "single",
    options: [
      { id: "defer", label: documentationText("Defer attachments", "推迟附件"), description: documentationText("Ship v1 two weeks earlier", "v1 提前两周交付"), tradeoff: documentationText("A second migration later", "后续需一次追加迁移") },
      { id: "include", label: documentationText("Include attachments", "包含附件"), description: documentationText("One complete format", "一次性完整格式"), tradeoff: documentationText("Size limits and scrubbing rules now", "现在就要定体积上限与脱敏规则") },
    ],
    allow_custom: true,
  },
};

const documentationPreviewStyles = `
body{font:15px/1.6 system-ui;margin:0;padding:24px;color:#1f2933;background:#f8fafc}
h1{font-size:20px;margin:0 0 4px}
p.lead{margin:0 0 16px;color:#52606d}
.grid{display:grid;grid-template-columns:1fr 1fr;gap:12px;margin-bottom:16px}
.card{background:#fff;border:1px solid #d9e2ec;border-radius:10px;padding:14px}
.card h2{font-size:13px;letter-spacing:.04em;margin:0 0 8px;color:#486581}
.card ul{margin:0;padding-left:18px}
.card p{margin:8px 0 0;font-size:13px;color:#52606d}
.pill{display:inline-block;padding:2px 10px;border-radius:999px;font-size:12px;font-weight:600}
.pill.settled{background:#e3f9e5;color:#207227}
.pill.open{background:#fff3c4;color:#8d6708}
.pill.ready{background:#e1f0ff;color:#1259a7}
.pill.blocked{background:#ffe3e3;color:#a61b1b}
.pill.done{background:#e3f9e5;color:#207227}
table{width:100%;border-collapse:collapse;background:#fff;border:1px solid #d9e2ec;margin-bottom:16px}
th,td{padding:8px 12px;text-align:left;border-top:1px solid #e4ebf1;font-size:14px}
th{border-top:none;color:#486581;font-size:12px}
figure{margin:0 0 16px;background:#fff;border:1px solid #d9e2ec;border-radius:10px;padding:14px}
figcaption{font-size:12px;color:#486581;margin-bottom:8px}
`;

function documentationBrief(title: string, lead: string, body: string) {
  return `<!doctype html>\n<html lang="${documentationText("en", "zh-CN")}"><head><meta charset="utf-8"><title>${title}</title><style>${documentationPreviewStyles}</style></head><body>\n<h1>${title}</h1>\n<p class="lead">${lead}</p>\n${body}\n</body></html>`;
}

const proposalBriefDocument = documentationBrief(
  documentationText("Proposal choice brief: compact export", "提案决策简报：紧凑导出"),
  documentationText("Every choice grounded in the proposal, split into settled boundaries and the decisions a human still owns.", "每个选择都锚定在提案上，分为已确认的边界与仍需人类拍板的决策。"),
  `<div class="grid">
<div class="card"><h2>${documentationText("Settled", "已确认")} <span class="pill settled">confirmed</span></h2><ul><li>${documentationText("Exports never include credentials or tokens.", "导出永不包含凭据或令牌。")}</li><li>${documentationText("One portable file per support request.", "每个支持请求对应一个可携文件。")}</li></ul><p>${documentationText("Verify the boundary; no need to re-decide.", "只需校验边界，无需重新决策。")}</p></div>
<div class="card"><h2>${documentationText("Needs your decision", "需要你拍板")} <span class="pill open">open</span></h2><ul><li>${documentationText("Include attachments in v1, or defer?", "v1 是否包含附件，还是推迟？")}</li><li>${documentationText("Answer on the QUESTION comment below.", "请在下方 QUESTION 评论上作答。")}</li></ul><p>${documentationText("Recommendation: defer — smaller surface, faster review.", "建议：推迟——面更小，review 更快。")}</p></div>
</div>
<table><tr><th>${documentationText("Option", "选项")}</th><th>${documentationText("Benefit", "收益")}</th><th>${documentationText("Cost", "代价")}</th></tr>
<tr><td>${documentationText("Defer attachments", "推迟附件")}</td><td>${documentationText("Ship v1 two weeks earlier", "v1 提前两周交付")}</td><td>${documentationText("A second migration later", "后续需一次追加迁移")}</td></tr>
<tr><td>${documentationText("Include attachments", "包含附件")}</td><td>${documentationText("One complete format", "一次性完整格式")}</td><td>${documentationText("Size limits and scrubbing rules now", "现在就要定体积上限与脱敏规则")}</td></tr></table>`,
);

const designBriefDocument = documentationBrief(
  documentationText("Design review: compact export", "设计评审：紧凑导出"),
  documentationText("How the data flows, which invariants hold, and which alternatives were rejected.", "数据如何流动、哪些不变量必须成立，以及否决了哪些方案。"),
  `<figure><figcaption>${documentationText("Export data flow", "导出数据流")}</figcaption>
<svg viewBox="0 0 640 96" width="100%" height="96" role="img">
<g font-family="system-ui" font-size="13" text-anchor="middle">
<rect x="8" y="28" width="140" height="40" rx="8" fill="#e1f0ff" stroke="#1259a7"/><text x="78" y="52" fill="#1259a7">${documentationText("Report store", "报告存储")}</text>
<rect x="196" y="28" width="150" height="40" rx="8" fill="#fff" stroke="#486581"/><text x="271" y="52" fill="#334e68">${documentationText("Schema allowlist", "Schema 白名单")}</text>
<rect x="394" y="28" width="110" height="40" rx="8" fill="#fff" stroke="#486581"/><text x="449" y="52" fill="#334e68">${documentationText("Scrubber", "脱敏器")}</text>
<rect x="552" y="28" width="80" height="40" rx="8" fill="#e3f9e5" stroke="#207227"/><text x="592" y="52" fill="#207227">${documentationText("Export", "导出件")}</text>
<g stroke="#829ab1" marker-end="none"><line x1="148" y1="48" x2="196" y2="48"/><line x1="346" y1="48" x2="394" y2="48"/><line x1="504" y1="48" x2="552" y2="48"/></g>
<g fill="#829ab1"><polygon points="196,48 188,44 188,52"/><polygon points="394,48 386,44 386,52"/><polygon points="552,48 544,44 544,52"/></g>
</g></svg></figure>
<div class="grid">
<div class="card"><h2>${documentationText("Invariants", "不变量")} <span class="pill settled">${documentationText("hold", "hold")}</span></h2><ul><li>${documentationText("Every exported field passes the allowlist.", "每个导出字段都经过白名单。")}</li><li>${documentationText("Scrubbing runs before serialization, not after.", "脱敏发生在序列化之前，而非之后。")}</li><li>${documentationText("Export failures never leave partial files.", "导出失败不留下半成品文件。")}</li></ul></div>
<div class="card"><h2>${documentationText("Alternatives rejected", "已否决方案")} <span class="pill open">${documentationText("context", "context")}</span></h2><ul><li>${documentationText("Denylist filtering — new fields leak by default.", "黑名单过滤——新字段默认泄漏。")}</li><li>${documentationText("Client-side scrubbing — cannot be audited server-side.", "客户端脱敏——服务端无法审计。")}</li></ul></div>
</div>
<table><tr><th>${documentationText("Acceptance check", "验收检查")}</th><th>${documentationText("Verified by", "验证方式")}</th></tr>
<tr><td>${documentationText("Credential-shaped strings never serialize", "凭据形态字符串永不序列化")}</td><td>${documentationText("Property test on the scrubber", "脱敏器性质测试")}</td></tr>
<tr><td>${documentationText("Allowlist stays the single source", "白名单保持唯一来源")}</td><td>${documentationText("Schema module round-trip test", "Schema 模块双向测试")}</td></tr></table>`,
);

const implementBriefDocument = documentationBrief(
  documentationText("Execution brief: compact export", "执行简报：紧凑导出"),
  documentationText("The PROCESS DAG at a glance: what runs in parallel, what is blocked, and who owns each node.", "一眼看清 PROCESS DAG：哪些可并行、哪些被阻塞、每个节点由谁负责。"),
  `<div class="grid">
<div class="card"><h2>${documentationText("Safely parallel now", "现在可安全并行")} <span class="pill ready">2 ${documentationText("ready", "就绪")}</span></h2><ul><li>PROCESS-001 · ${documentationText("schema allowlist module", "schema 白名单模块")}</li><li>PROCESS-004 · ${documentationText("scrubber property tests", "脱敏器性质测试")}</li></ul><p>${documentationText("Disjoint files, no shared touchpoints.", "文件不相交，无共享触点。")}</p></div>
<div class="card"><h2>${documentationText("Blocked", "被阻塞")} <span class="pill blocked">1 ${documentationText("waiting", "等待中")}</span></h2><ul><li>PROCESS-002 · ${documentationText("export endpoint — waits on PROCESS-001", "导出接口——等待 PROCESS-001")}</li></ul><p>${documentationText("Shared touchpoint: the schema module API.", "共享触点：schema 模块 API。")}</p></div>
</div>
<table><tr><th>PROCESS</th><th>${documentationText("Scope", "范围")}</th><th>${documentationText("Worker", "实现方")}</th><th>${documentationText("Reviewer", "评审方")}</th><th>${documentationText("Depends on", "依赖")}</th><th>${documentationText("State", "状态")}</th></tr>
<tr><td>PROCESS-001</td><td>${documentationText("Schema allowlist", "Schema 白名单")}</td><td>codex-worker</td><td>claude-reviewer</td><td>—</td><td><span class="pill ready">${documentationText("active", "进行中")}</span></td></tr>
<tr><td>PROCESS-002</td><td>${documentationText("Export endpoint", "导出接口")}</td><td>codex-worker</td><td>claude-reviewer</td><td>PROCESS-001</td><td><span class="pill blocked">${documentationText("blocked", "阻塞")}</span></td></tr>
<tr><td>PROCESS-003</td><td>${documentationText("CLI surface", "CLI 入口")}</td><td>qoder-worker</td><td>codex-reviewer</td><td>PROCESS-002</td><td><span class="pill open">${documentationText("queued", "排队中")}</span></td></tr>
<tr><td>PROCESS-004</td><td>${documentationText("Scrubber tests", "脱敏测试")}</td><td>claude-worker</td><td>codex-reviewer</td><td>—</td><td><span class="pill done">${documentationText("done", "完成")}</span></td></tr></table>
<p class="lead">${documentationText("Estimates and lane suggestions are planning aids; typed PROCESS comments stay authoritative.", "估算与并行建议仅供规划参考；类型化 PROCESS 评论仍是权威数据。")}</p>`,
);

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
    if (url.pathname === "/api/v1/meta") return route.fulfill({ json: { api_version: "v1", features: { bootstrap: true, personal_access_tokens: true, organizations: true, source_bindings: false, webhooks: false, change_boards: false, runner: true, recovery_exchange: true, search: true, email_notifications: true, repository_email_subscriptions: true, html_preview_execution: true, interactive_question_answers: true } } });
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
    if (/^\/repos\/acme\/workflow\/issues\/comments\/\d+$/.test(url.pathname) && request.method() === "DELETE") {
      const commentId = Number(url.pathname.split("/").pop());
      comments = comments.filter((comment) => comment.id !== commentId);
      activeIssue = { ...activeIssue, comments: comments.length };
      return route.fulfill({ status: 204 });
    }
    if (url.pathname === "/repos/acme/workflow/issues/comments/9/reactions") return route.fulfill({ json: [{ id: 7, user: user, content: "+1", created_at: "2026-07-10T12:00:00Z" }] });
    if (/^\/repos\/acme\/workflow\/issues\/comments\/\d+\/reactions$/.test(url.pathname)) return route.fulfill({ json: [] });
    if (url.pathname === "/api/v1/context/repos/acme/workflow/issues/41/relationships") return route.fulfill({ json: { relationships: externalContributor ? [] : relationships } });
    if (/^\/api\/v1\/repos\/acme\/workflow\/issues\/41\/previews\/(?:review-lab|comment-lab|issue-first|anchored-first|proposal-brief|design-brief|implement-brief)$/.test(url.pathname)) {
      previewRequests += 1;
      const commentPreview = url.pathname.endsWith("/comment-lab") || url.pathname.endsWith("/anchored-first");
      expect(url.searchParams.get("source")).toBe(commentPreview ? "comment" : "issue");
      expect(url.searchParams.get("comment_id")).toBe(commentPreview ? "9" : null);
      expect(url.searchParams.get("digest")).toMatch(/^[0-9a-f]{64}$/);
      const briefDocuments: Record<string, string> = { "proposal-brief": proposalBriefDocument, "design-brief": designBriefDocument, "implement-brief": implementBriefDocument };
      const briefId = url.pathname.split("/").pop() ?? "";
      const document = url.pathname.endsWith("/anchored-first") ? "<!doctype html><p>comment</p>" : briefDocuments[briefId] ?? previewDocument;
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
    if (url.pathname === "/api/v1/context/repos/acme/workflow/issues/search") return route.fulfill({ json: {
      items: [{
        organization_id: organizationId,
        organization: "acme",
        repository_id: repositoryId,
        repository: "workflow",
        id: "dddddddd-dddd-4ddd-8ddd-dddddddddddd",
        number: 41,
        title: issueTitle,
        state: "open",
        updated_at: "2026-07-10T10:00:00Z",
        url: "http://127.0.0.1:4173/acme/workflow/issues/41",
        changes: [],
        score: 50000,
        matches: [{
          source: "comment",
          comment_id: "eeeeeeee-eeee-4eee-8eee-eeeeeeeeeeee",
          excerpt: documentationText(
            "The rollout token exists only in this comment, so repository search can still find the discussion.",
            "发布令牌只存在于这条评论中，因此仓库搜索仍能找到这段讨论。",
          ),
        }],
      }],
      page: 1,
      per_page: 20,
      total: 1,
      has_next: false,
    } });
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

test("highlighted JSON keeps readable contrast on its code block", async ({ page }, testInfo) => {
  test.skip(testInfo.project.name !== "issues-desktop-1440");
  activeIssue = {
    ...issue,
    body: `## Representative error

\`\`\`json
{
  "error": {
    "message": "The requested model does not exist",
    "type": "invalid_request_error",
    "param": null,
    "code": "model_not_found"
  }
}
\`\`\``,
  };

  await page.goto("/acme/workflow/issues/41");
  const code = page.locator("code.language-json");
  await expect(code).toBeVisible();
  const contrast = await code.evaluate((element) => {
    const parseColor = (value: string) => (value.match(/[\d.]+/g) ?? []).slice(0, 3).map(Number);
    const luminance = (value: string) => {
      const [red, green, blue] = parseColor(value).map((channel) => {
        const normalized = channel / 255;
        return normalized <= .04045 ? normalized / 12.92 : ((normalized + .055) / 1.055) ** 2.4;
      });
      return .2126 * red + .7152 * green + .0722 * blue;
    };
    const background = getComputedStyle(element.closest("pre")!).backgroundColor;
    const backgroundLuminance = luminance(background);
    return [element, ...element.querySelectorAll(".hljs-attr, .hljs-string, .hljs-literal")]
      .map((token) => {
        const tokenLuminance = luminance(getComputedStyle(token).color);
        const lighter = Math.max(backgroundLuminance, tokenLuminance);
        const darker = Math.min(backgroundLuminance, tokenLuminance);
        return (lighter + .05) / (darker + .05);
      })
      .reduce((lowest, value) => Math.min(lowest, value), Number.POSITIVE_INFINITY);
  });
  expect(contrast).toBeGreaterThanOrEqual(4.5);
});

test("comment deletion requires trusted confirmation and converges after success", async ({ page }, testInfo) => {
  test.skip(testInfo.project.name !== "issues-desktop-1440");
  const body = documentationText(
    "The runner should preserve **raw Markdown** and agent metadata.",
    "运行器应保留**原始 Markdown**和智能体元数据。",
  );
  await page.goto("/acme/workflow/issues/41");
  const comment = page.locator("#issuecomment-9");
  await expect(comment).toContainText(documentationText("The runner should preserve", "运行器应保留"));

  await comment.getByRole("button", { name: documentationText("Delete", "删除") }).click();
  const confirmation = page.getByRole("dialog", { name: documentationText("Delete this comment?", "删除这条评论？") });
  await expect(confirmation).toBeVisible();
  await expect(confirmation).not.toContainText(body);
  await confirmation.getByRole("button", { name: documentationText("Cancel", "取消") }).click();
  await expect(confirmation).toHaveCount(0);
  await expect(comment).toBeVisible();

  await comment.getByRole("button", { name: documentationText("Delete", "删除") }).click();
  await confirmation.getByRole("button", { name: documentationText("Delete comment", "删除评论") }).click();
  await expect(comment).toHaveCount(0);
  await expect(page.getByText(documentationText("0 comments", "0 条评论"))).toBeVisible();
  const accessibility = await new AxeBuilder({ page }).analyze();
  expect(accessibility.violations).toEqual([]);
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
  const flowchartDataUrl = await diagrams.nth(0).getAttribute("src") ?? "";
  const flowchartSource = decodeURIComponent(flowchartDataUrl.split(",", 2)[1] ?? "");
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
  const openButtons = page.getByRole("button", { name: documentationText("Open Mermaid diagram in enlarged view", "在放大视图中打开 Mermaid 图表") });
  await expect(openButtons).toHaveCount(2);
  await openButtons.nth(0).click();
  const viewer = page.getByRole("dialog", { name: documentationText("Mermaid diagram viewer", "Mermaid 图表查看器") });
  await expect(viewer).toBeVisible();
  const expandedDiagram = viewer.getByRole("img", { name: documentationText("Mermaid diagram", "Mermaid 图表") });
  await expect(expandedDiagram).toHaveAttribute("src", flowchartDataUrl);
  await viewer.getByRole("button", { name: documentationText("Zoom in", "放大") }).click();
  await expect(viewer.getByText("125%")).toBeVisible();
  await expect(expandedDiagram).toHaveAttribute("style", /width: 125%/);
  const viewerAccessibility = await new AxeBuilder({ page }).analyze();
  expect(viewerAccessibility.violations).toEqual([]);
  await page.keyboard.press("Escape");
  await expect(viewer).toBeHidden();
  await expect(page.locator("#issuecomment-9 code.language-mermaid")).toHaveCount(0);
  const overflow = await page.evaluate(() => document.documentElement.scrollWidth - document.documentElement.clientWidth);
  expect(overflow).toBeLessThanOrEqual(1);
  const accessibility = await new AxeBuilder({ page }).analyze();
  expect(accessibility.violations).toEqual([]);
});

test("sandboxed review previews render directly, block authority, and keep QUESTION answers native", async ({ page }) => {
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
\`\`\``), commentFixture(20, `Which release posture should we use?

<!-- issue-spec:type=QUESTION id=QUESTION-007 version=1 -->`)];
  await page.goto("/acme/workflow/issues/41");
  const iframe = page.locator('iframe[title="Review lab"]');
  const commentIframe = page.locator('iframe[title="Comment lab"]');
  await expect(iframe).toHaveCount(1);
  await expect(commentIframe).toHaveCount(1);
  expect(previewRequests).toBe(2);
  const mermaid = page.getByRole("img", { name: documentationText("Mermaid diagram", "Mermaid 图表") });
  await expect(mermaid).toBeVisible();
  const mermaidSource = await mermaid.getAttribute("src");

  await expect(iframe).toHaveAttribute("height", "720");
  await expect(iframe).toHaveAttribute("sandbox", "allow-scripts");
  await expect(iframe).toHaveAttribute("referrerpolicy", "no-referrer");
  await expect(iframe).not.toHaveAttribute("srcdoc", /.+/);
  expect(previewRequests).toBe(2);
  await iframe.evaluate((element) => { element.setAttribute("data-stability-probe", "original"); });
  const frame = page.frameLocator('iframe[title="Review lab"]');
  await expect(frame.locator("#capability")).toHaveText("display-only");
  await frame.getByRole("button", { name: "Increment" }).click();
  await expect(frame.locator("#count")).toHaveText("1");
  for (const id of ["parent-access", "cookie-access", "local-storage", "session-storage", "network-access", "popup-access", "worker-access", "image-access"]) {
    await expect(frame.locator(`#${id}`)).toHaveText("blocked");
  }
  const pageURL = page.url();
  await frame.getByRole("button", { name: "Blocked form" }).click();
  await frame.getByRole("link", { name: "Blocked navigation" }).click();
  await expect(page).toHaveURL(pageURL);
  await expect(frame.locator("#capability")).toHaveText("display-only");

  const iframeBox = await iframe.boundingBox();
  expect(iframeBox).not.toBeNull();
  expect((iframeBox?.x ?? 0) + (iframeBox?.width ?? 0)).toBeLessThanOrEqual((page.viewportSize()?.width ?? 0) + 1);
  expect(await page.evaluate(() => document.documentElement.scrollWidth - document.documentElement.clientWidth)).toBeLessThanOrEqual(1);
  expect((await mermaid.getAttribute("src"))).toBe(mermaidSource);
  const accessibility = await new AxeBuilder({ page })
    .exclude(".danger-text")
    .exclude(".question-blocking-badge")
    .analyze();
  expect(accessibility.violations).toEqual([]);

  const commentFrame = page.frameLocator('iframe[title="Comment lab"]');
  await expect(commentIframe).toHaveCount(1);
  await commentIframe.evaluate((element) => { element.setAttribute("data-stability-probe", "comment-original"); });
  await expect(commentFrame.locator("#capability")).toHaveText("display-only");
  await commentFrame.getByRole("button", { name: "Increment" }).click();
  await expect(commentFrame.locator("#count")).toHaveText("1");
  await page.waitForTimeout(1_100);
  await expect(commentIframe).toHaveAttribute("data-stability-probe", "comment-original");
  await expect(commentFrame.locator("#count")).toHaveText("1");
  await expect(commentFrame.locator("#mount-count")).toHaveText("1");
  expect(previewRequests).toBe(2);

  const questionPanel = page.getByRole("region", { name: /QUESTION-007/ });
  await questionPanel.getByRole("radio", { name: /Safe/ }).click();
  await questionPanel.getByRole("button", { name: documentationText("Submit answer", "提交答案") }).click();
  await expect(page.getByText("ANSWER timeline 1")).toBeVisible();
  await expect(page.getByRole("dialog")).toHaveCount(0);
  await expect(iframe).toHaveAttribute("data-stability-probe", "original");
  await expect(frame.locator("#mount-count")).toHaveText("1");
  await expect(mermaid).toHaveAttribute("src", mermaidSource ?? "");
  expect(answerSubmissions).toEqual([
    { question_id: "QUESTION-007", question_digest: "a".repeat(64), option_ids: ["safe"], custom: "" },
  ]);
});

test("all previews render when opening a comment anchor", async ({ page }, testInfo) => {
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
  await expect(page.locator('iframe[title="Anchored first"]')).toHaveCount(1);
  await expect(page.locator('iframe[title="Issue first"]')).toHaveCount(1);
  await expect(page.frameLocator('iframe[title="Anchored first"]').getByText("comment", { exact: true })).toBeVisible();
  expect(previewRequests).toBe(2);
});

test("proposal choice brief documentation screenshot", async ({ page }, testInfo) => {
  test.skip(testInfo.project.name !== "issues-desktop-1440");
  const briefTitle = documentationText("Proposal choice brief", "提案决策简报");
  activeIssue = {
    ...issue,
    title: documentationText("Proposal: compact-export", "Proposal：compact-export"),
    labels: [{ id: 3, name: "issue-spec/proposal", color: "0969da", default: false, description: "Proposal", url: "" }],
    body: documentationText(
      `## Proposal: compact export\n\nSupport requests need a small, portable export. The brief below is the human review surface; the proposal body and typed comments stay authoritative.`,
      `## 提案：紧凑导出\n\n支持请求需要小巧可携的导出件。下方简报是人类评审界面；提案正文与类型化评论仍是权威数据。`,
    ) + `\n\n\`\`\`html-preview id=proposal-brief version=1 title="${briefTitle}" height=560\n${proposalBriefDocument}\n\`\`\``,
  };
  comments = [commentFixture(20, documentationText(
    `<!-- issue-spec:type=QUESTION id=QUESTION-009 version=1 -->\nAgent: Requirements\nType: QUESTION\nID: QUESTION-009\nStatus: open\nScope: export scope\nLinks: SPEC-001\n\n## Question\nShould attachments be included in the first compact-export version?`,
    `<!-- issue-spec:type=QUESTION id=QUESTION-009 version=1 -->\nAgent: Requirements\nType: QUESTION\nID: QUESTION-009\nStatus: open\nScope: 导出范围\nLinks: SPEC-001\n\n## Question\n紧凑导出第一版是否包含附件？`,
  ))];

  await page.goto("/acme/workflow/issues/41");
  await expect(page.getByRole("heading", { level: 1 }).first()).toContainText(activeIssue.title);
  await expect(page.frameLocator(`iframe[title="${briefTitle}"]`).getByText(documentationText("Proposal choice brief: compact export", "提案决策简报：紧凑导出"))).toBeVisible();
  const panel = page.getByRole("region", { name: /QUESTION-009/ });
  await expect(panel.getByRole("radio", { name: new RegExp(documentationText("Defer attachments", "推迟附件")) })).toBeVisible();
  await expect(panel.getByRole("radio", { name: new RegExp(documentationText("Include attachments", "包含附件")) })).toBeVisible();
  await page.evaluate(() => (document.activeElement as HTMLElement | null)?.blur());
  await stabilizeScreenshotDates(page);
  await expect(page).toHaveScreenshot(documentationSnapshot("review-projection-proposal"), { fullPage: true, animations: "disabled" });
});

test("design explainer documentation screenshot", async ({ page }, testInfo) => {
  test.skip(testInfo.project.name !== "issues-desktop-1440");
  const briefTitle = documentationText("Design explainer", "设计评审简报");
  activeIssue = {
    ...issue,
    title: documentationText("Design: compact-export", "Design：compact-export"),
    labels: [labels[0]],
    body: documentationText(
      `## Design: compact export\n\nThe export ships behind one schema allowlist module. The explainer below is the human review surface; the design body and typed comments stay authoritative.`,
      `## 设计：紧凑导出\n\n导出能力收敛在一个 schema 白名单模块之后。下方简报是人类评审界面；设计正文与类型化评论仍是权威数据。`,
    ) + `\n\n\`\`\`html-preview id=design-brief version=1 title="${briefTitle}" height=700\n${designBriefDocument}\n\`\`\``,
  };
  comments = [commentFixture(21, documentationText(
    "The denylist alternative is documented in the explainer; the allowlist stays the single source.",
    "黑名单方案已在简报中记录；白名单保持唯一来源。",
  ))];

  await page.goto("/acme/workflow/issues/41");
  await expect(page.getByRole("heading", { level: 1 }).first()).toContainText(activeIssue.title);
  await expect(page.frameLocator(`iframe[title="${briefTitle}"]`).getByText(documentationText("Design review: compact export", "设计评审：紧凑导出"))).toBeVisible();
  await expect(page.frameLocator(`iframe[title="${briefTitle}"]`).getByRole("heading", { name: new RegExp(documentationText("Invariants", "不变量")) })).toBeVisible();
  await page.evaluate(() => (document.activeElement as HTMLElement | null)?.blur());
  await stabilizeScreenshotDates(page);
  await expect(page).toHaveScreenshot(documentationSnapshot("review-projection-design"), { fullPage: true, animations: "disabled" });
});

test("implement execution brief documentation screenshot", async ({ page }, testInfo) => {
  test.skip(testInfo.project.name !== "issues-desktop-1440");
  const briefTitle = documentationText("Execution brief", "执行简报");
  activeIssue = {
    ...issue,
    title: documentationText("Implement: compact-export", "Implement：compact-export"),
    labels: [{ id: 4, name: "issue-spec/implement", color: "1a7f37", default: false, description: "Implement", url: "" }],
    body: documentationText(
      `## Implement: compact export\n\nFour PROCESS nodes, two safely parallel. The brief below is the human review surface; typed PROCESS comments stay authoritative.`,
      `## 实现：紧凑导出\n\n四个 PROCESS 节点，两个可安全并行。下方简报是人类评审界面；类型化 PROCESS 评论仍是权威数据。`,
    ) + `\n\n\`\`\`html-preview id=implement-brief version=1 title="${briefTitle}" height=720\n${implementBriefDocument}\n\`\`\``,
  };
  comments = [commentFixture(22, documentationText(
    `<!-- issue-spec:type=PROCESS id=PROCESS-001 version=2 -->\nAgent: Coordinator\nType: PROCESS\nID: PROCESS-001\nStatus: active\nScope: schema allowlist module\nLinks: TASK-001, SPEC-001\n\n## Work\nOwn the schema allowlist module and its round-trip test.`,
    `<!-- issue-spec:type=PROCESS id=PROCESS-001 version=2 -->\nAgent: Coordinator\nType: PROCESS\nID: PROCESS-001\nStatus: active\nScope: schema 白名单模块\nLinks: TASK-001, SPEC-001\n\n## Work\n负责 schema 白名单模块及其双向测试。`,
  ))];

  await page.goto("/acme/workflow/issues/41");
  await expect(page.getByRole("heading", { level: 1 }).first()).toContainText(activeIssue.title);
  await expect(page.frameLocator(`iframe[title="${briefTitle}"]`).getByText(documentationText("Execution brief: compact export", "执行简报：紧凑导出"))).toBeVisible();
  await expect(page.frameLocator(`iframe[title="${briefTitle}"]`).getByText("PROCESS-002").first()).toBeVisible();
  await page.evaluate(() => (document.activeElement as HTMLElement | null)?.blur());
  await stabilizeScreenshotDates(page);
  await expect(page).toHaveScreenshot(documentationSnapshot("review-projection-implement"), { fullPage: true, animations: "disabled" });
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

test("repository full-discussion search presents comment matches", async ({ page }, testInfo) => {
  test.skip(testInfo.project.name !== "issues-desktop-1440");
  await page.goto("/acme/workflow/issues");
  await page.getByRole("searchbox", { name: documentationText("Search issue titles, bodies, and comments", "搜索议题标题、主体和评论") }).fill("rollout token");
  await page.getByRole("button", { name: documentationText("Search", "搜索"), exact: true }).click();
  await expect(page).toHaveURL(/q=rollout\+token/);
  await expect(page.getByText(documentationText("The rollout token exists only in this comment, so repository search can still find the discussion.", "发布令牌只存在于这条评论中，因此仓库搜索仍能找到这段讨论。"))).toBeVisible();
  const accessibility = await new AxeBuilder({ page }).analyze();
  expect(accessibility.violations).toEqual([]);
  await page.evaluate(() => (document.activeElement as HTMLElement | null)?.blur());
  await stabilizeScreenshotDates(page);
  await expect(page).toHaveScreenshot(documentationSnapshot("issue-list-search"), { fullPage: true, animations: "disabled" });
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
