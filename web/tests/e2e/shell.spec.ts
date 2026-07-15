import AxeBuilder from "@axe-core/playwright";
import { expect, test } from "@playwright/test";
import { fixtureContext, fixtureMeta } from "../server";
import { documentationSnapshot, documentationText, installDocumentationLanguage } from "./documentation-language";

const repositoryId = "cccccccc-cccc-4ccc-8ccc-cccccccccccc";
const serviceAccountId = "dddddddd-dddd-4ddd-8ddd-dddddddddddd";
const serviceAccountLogin = "svc-runner-bot-a1b2c3d4";

test.beforeEach(async ({ page }) => {
  await installDocumentationLanguage(page);
  await page.route("**/api/v1/**", async (route) => {
    const url = new URL(route.request().url());
    if (url.pathname === "/api/v1/meta") return route.fulfill({ json: fixtureMeta });
    if (url.pathname === "/api/v1/context") return route.fulfill({ json: fixtureContext });
    if (url.pathname.includes("/api/v1/context/orgs/") && url.pathname.endsWith("/repos")) {
      return route.fulfill({ json: { repositories: [{ repository: { id: repositoryId, organization_id: fixtureContext.organizations[0].id, name: "workflow", display_name: "Workflow", visibility: "private", contribution_policy: "members" }, effective_permission: "admin", allowed_actions: ["read", "contribute", "triage", "write", "integrations.manage", "repository.admin"] }] } });
    }
    if (url.pathname === `/api/v1/orgs/${fixtureContext.organizations[0].id}/repos`) return route.fulfill({ json: { repositories: [{ id: repositoryId, organization_id: fixtureContext.organizations[0].id, name: "workflow", display_name: "Workflow", visibility: "private", default_branch: "main", contribution_policy: "members", representation_version: 1 }] } });
    if (url.pathname === `/api/v1/orgs/${fixtureContext.organizations[0].id}/user-candidates`) return route.fulfill({ json: { users: [{ id: serviceAccountId, login: serviceAccountLogin, display_name: "Runner Bot", kind: "service_account", status: "active" }] } });
    if (url.pathname === `/api/v1/orgs/${fixtureContext.organizations[0].id}/users/${serviceAccountId}/pats`) return route.fulfill({ json: { tokens: [] } });
    return route.fulfill({ status: 404, contentType: "application/problem+json", body: JSON.stringify({ status: 404, title: "Not found", code: "not_found", request_id: "playwright-request" }) });
  });
});

test("runner managed PAT keeps the service identity and repository cap explicit", async ({ page }, testInfo) => {
  test.skip(testInfo.project.name !== "desktop-1440");
  await page.goto(`/admin/orgs/${fixtureContext.organizations[0].id}/managed-tokens`);
  await page.getByRole("textbox", { name: documentationText("Exact local login", "准确的本地登录名") }).fill(serviceAccountLogin);
  await page.getByRole("button", { name: documentationText("Resolve", "确认用户") }).click();
  await expect(page.getByText(`@${serviceAccountLogin}`)).toBeVisible();
  await page.getByRole("button", { name: documentationText("Runner preset", "运行器预设") }).click();
  await page.getByRole("combobox", { name: documentationText("Repository access", "仓库范围") }).selectOption(repositoryId);
  await expect(page.getByRole("textbox", { name: documentationText("Token name", "令牌名称") })).toHaveValue("runner");
  await expect(page.getByRole("textbox", { name: documentationText("Scopes", "权限范围") })).toHaveValue("read:user, issues:read, issues:write, runner:delegate, evidence:write");
  expect(await page.evaluate(() => document.documentElement.scrollWidth - document.documentElement.clientWidth)).toBeLessThanOrEqual(1);
  expect((await new AxeBuilder({ page }).analyze()).violations).toEqual([]);
  await page.evaluate(() => (document.activeElement as HTMLElement | null)?.blur());
  await page.locator(".skip-link").evaluate((node) => node.remove());
  await expect(page).toHaveScreenshot(documentationSnapshot("runner-service-account"), { fullPage: true, animations: "disabled" });
});

test("responsive shell remains accessible and visually stable", async ({ page }, testInfo) => {
  await page.goto("/");
  await expect(page.getByRole("heading", { name: documentationText("Good work starts with orientation", "知止而后有定，定而后能静。") })).toBeVisible();
  const overflow = await page.evaluate(() => document.documentElement.scrollWidth - document.documentElement.clientWidth);
  expect(overflow).toBeLessThanOrEqual(1);
  const accessibility = await new AxeBuilder({ page }).analyze();
  expect(accessibility.violations).toEqual([]);
  await page.keyboard.press("Tab");
  await expect(page.getByRole("link", { name: documentationText("Skip to main content", "跳至主要内容") })).toBeFocused();
  const toggle = page.getByRole("button", { name: documentationText("Inspector", "请求检视"), exact: true });
  const mainWidth = await page.locator("#main-content").evaluate((element) => element.getBoundingClientRect().width);
  await expect(toggle).toHaveAttribute("aria-expanded", "false");
  await toggle.click();
  await expect(toggle).toHaveAttribute("aria-expanded", "true");
  const closeInspector = page.getByRole("button", { name: documentationText("Close inspector", "关闭请求检视") });
  await expect(closeInspector).toBeFocused();
  expect(await page.locator("#main-content").evaluate((element) => element.getBoundingClientRect().width)).toBe(mainWidth);
  await closeInspector.click();
  await expect(toggle).toBeFocused();
  if (testInfo.project.name === "mobile-390") {
    const menu = page.getByRole("button", { name: documentationText("Toggle navigation", "展开或收起导航") });
    await menu.click();
    await expect(menu).toHaveAttribute("aria-expanded", "true");
    await page.getByRole("link", { name: documentationText("Repositories", "仓库") }).click();
    await expect(menu).toHaveAttribute("aria-expanded", "false");
  }
  await page.evaluate(() => (document.activeElement as HTMLElement | null)?.blur());
  await expect(page).toHaveScreenshot(documentationSnapshot("dashboard"), { fullPage: true, animations: "disabled" });
});

test("200 percent equivalent reflow has no horizontal clipping", async ({ page }, testInfo) => {
  test.skip(testInfo.project.name !== "desktop-1440");
  await page.setViewportSize({ width: 720, height: 900 });
  await page.goto("/");
  await expect(page.getByRole("heading", { name: documentationText("Good work starts with orientation", "知止而后有定，定而后能静。") })).toBeVisible();
  const overflow = await page.evaluate(() => document.documentElement.scrollWidth - document.documentElement.clientWidth);
  expect(overflow).toBeLessThanOrEqual(1);
  await expect(page).toHaveScreenshot(documentationSnapshot("dashboard-200-percent"), { fullPage: true, animations: "disabled" });
});
