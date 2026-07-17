import { expect, test } from "@playwright/test";
import { fixtureContext, fixtureMeta } from "../server";
import { documentationSnapshot, documentationText, installDocumentationLanguage } from "./documentation-language";

const repositoryId = "cccccccc-cccc-4ccc-8ccc-cccccccccccc";
const syntheticSecret = "[SYNTHETIC REDACTED — NOT A CREDENTIAL]";

test.beforeEach(async ({ page }) => {
  await installDocumentationLanguage(page);
  await page.route("**/api/v1/**", async (route) => {
    const request = route.request();
    const url = new URL(request.url());
    if (url.pathname === "/api/v1/meta") return route.fulfill({ json: fixtureMeta });
    if (url.pathname === "/api/v1/context") return route.fulfill({ json: {
      ...fixtureContext,
      user: { ...fixtureContext.user, site_admin: false },
      allowed_actions: [],
      organizations: fixtureContext.organizations.map((organization) => ({ ...organization, effective_permission: "read", allowed_actions: ["organization.read"] })),
    } });
    if (url.pathname === "/api/v1/pats" && request.method() === "GET") return route.fulfill({ json: { tokens: [] } });
    if (url.pathname === "/api/v1/pats" && request.method() === "POST") return route.fulfill({ status: 201, json: { token: syntheticSecret } });
    if (url.pathname === `/api/v1/context/orgs/${fixtureContext.organizations[0].id}/repos`) {
      return route.fulfill({ json: { repositories: [{
        repository: { id: repositoryId, organization_id: fixtureContext.organizations[0].id, name: "widgets", display_name: "Widgets", visibility: "public", contribution_policy: "public" },
        effective_permission: "read",
        allowed_actions: ["read", "contribute"],
      }] } });
    }
    return route.fulfill({ status: 404, contentType: "application/problem+json", body: JSON.stringify({ status: 404, title: "Not found", code: "not_found", request_id: "synthetic-requirements" }) });
  });
});

test("release evidence describes a verified non-piped install", async ({ page }, testInfo) => {
  test.skip(testInfo.project.name !== "desktop-1440");
  const copy = {
    eyebrow: documentationText("Synthetic release fixture", "合成 Release Fixture"),
    title: documentationText("issue-spec v1.8.0", "issue-spec v1.8.0"),
    immutable: documentationText("Immutable semantic release", "不可变语义版本 Release"),
    verified: documentationText("Verified before execution", "执行前已完成校验"),
    description: documentationText(
      "Installer and archive match the manifest, SHA-256 checksums, and GitHub attestation.",
      "安装器和压缩包均与 Manifest、SHA-256 Checksum 和 GitHub Attestation 一致。",
    ),
    version: documentationText("Version identity", "版本身份"),
    rerun: documentationText("Second native install: identical", "第二次原生安装：结果一致"),
  };
  await page.setContent(`<!doctype html><html lang="en"><head><meta charset="utf-8"><style>
    :root { color: #153d3a; background: #edf3ef; font-family: Inter, ui-sans-serif, system-ui, sans-serif; }
    body { margin: 0; min-height: 900px; display: grid; place-items: center; background: radial-gradient(circle at 18% 15%, #fff 0, #edf3ef 47%, #d9e8e1 100%); }
    main { width: 980px; border: 1px solid #b8cbc2; border-radius: 24px; background: rgba(255,255,255,.94); padding: 52px 58px; box-shadow: 0 24px 70px rgba(19,61,57,.13); }
    .eyebrow { color: #b65437; font-weight: 800; text-transform: uppercase; letter-spacing: .12em; }
    h1 { margin: 12px 0 8px; font: 700 46px/1.1 Georgia, serif; } p { color: #50635e; font-size: 18px; }
    .badges { display:flex; gap:12px; margin: 26px 0; } .badge { border-radius:999px; padding:9px 14px; background:#dff1e9; color:#176d67; font-weight:700; }
    .grid { display:grid; grid-template-columns: 1.3fr .7fr; gap:22px; margin-top:28px; }
    section { border:1px solid #d6e0dc; border-radius:16px; padding:22px; background:#fbfdfc; }
    h2 { margin:0 0 14px; font-size:18px; } pre { margin:0; white-space:pre-wrap; font: 15px/1.7 ui-monospace, monospace; color:#234d49; }
    ul { margin:0; padding-left:22px; line-height:2; } strong { color:#176d67; }
  </style></head><body><main>
    <div class="eyebrow">${copy.eyebrow}</div><h1>${copy.title}</h1><p>${copy.description}</p>
    <div class="badges"><span class="badge">✓ ${copy.immutable}</span><span class="badge">✓ ${copy.verified}</span></div>
    <div class="grid"><section><h2>${copy.version}</h2><pre>{
  "version": "1.8.0",
  "revision": "0123456789ab",
  "channel": "stable",
  "platform": "linux/amd64"
}</pre></section><section><h2>manifest.json</h2><ul><li>install.sh <strong>verified</strong></li><li>archive <strong>verified</strong></li><li>SHA256SUMS <strong>verified</strong></li><li>${copy.rerun}</li></ul></section></div>
  </main></body></html>`);
  await expect(page).toHaveScreenshot(documentationSnapshot("requirements-release"), { fullPage: true, animations: "disabled" });
});

test("requirements PAT uses the name-only defaults and a redacted one-time secret", async ({ page }, testInfo) => {
  test.skip(testInfo.project.name !== "desktop-1440");
  await page.goto("/settings/tokens?mode=requirements");
  const advanced = page.locator("details");
  await expect(advanced).not.toHaveAttribute("open", "");
  await page.getByRole("textbox", { name: documentationText("Token name", "令牌名称") }).fill(documentationText("requirements CLI", "需求 CLI"));
  await page.getByRole("button", { name: documentationText("Create token", "创建令牌") }).click();
  const dialog = page.getByRole("dialog", { name: documentationText("Save this access token", "请保存此访问令牌") });
  await expect(dialog).toContainText(syntheticSecret);
  await expect(advanced).not.toHaveAttribute("open", "");
  await page.evaluate(() => (document.activeElement as HTMLElement | null)?.blur());
  await expect(page).toHaveScreenshot(documentationSnapshot("requirements-pat-secret"), { fullPage: true, animations: "disabled" });
});
