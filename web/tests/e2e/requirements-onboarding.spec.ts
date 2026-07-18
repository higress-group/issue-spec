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
