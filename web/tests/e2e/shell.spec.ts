import AxeBuilder from "@axe-core/playwright";
import { expect, test } from "@playwright/test";
import { fixtureContext, fixtureMeta } from "../server";

test.beforeEach(async ({ page }) => {
  await page.route("**/api/v1/**", async (route) => {
    const url = new URL(route.request().url());
    if (url.pathname === "/api/v1/meta") return route.fulfill({ json: fixtureMeta });
    if (url.pathname === "/api/v1/context") return route.fulfill({ json: fixtureContext });
    if (url.pathname.includes("/api/v1/context/orgs/") && url.pathname.endsWith("/repos")) {
      return route.fulfill({ json: { repositories: [{ repository: { id: "cccccccc-cccc-4ccc-8ccc-cccccccccccc", organization_id: fixtureContext.organizations[0].id, name: "workflow", display_name: "Workflow", visibility: "private", contribution_policy: "members" }, effective_permission: "admin", allowed_actions: ["read", "contribute", "triage", "write", "integrations.manage", "repository.admin"] }] } });
    }
    return route.fulfill({ status: 404, contentType: "application/problem+json", body: JSON.stringify({ status: 404, title: "Not found", code: "not_found", request_id: "playwright-request" }) });
  });
});

test("responsive shell remains accessible and visually stable", async ({ page }, testInfo) => {
  await page.goto("/");
  await expect(page.getByRole("heading", { name: "Good work starts with orientation" })).toBeVisible();
  const overflow = await page.evaluate(() => document.documentElement.scrollWidth - document.documentElement.clientWidth);
  expect(overflow).toBeLessThanOrEqual(1);
  const accessibility = await new AxeBuilder({ page }).analyze();
  expect(accessibility.violations).toEqual([]);
  await page.keyboard.press("Tab");
  await expect(page.getByRole("link", { name: "Skip to main content" })).toBeFocused();
  if (testInfo.project.name === "desktop-1440") {
    await expect(page.getByRole("complementary", { name: "Request inspector" })).toBeVisible();
  } else {
    const toggle = page.getByRole("button", { name: "Inspector", exact: true });
    await expect(toggle).toHaveAttribute("aria-expanded", "false");
    await toggle.click();
    await expect(toggle).toHaveAttribute("aria-expanded", "true");
    await page.getByRole("button", { name: "Close inspector" }).click();
    await expect(toggle).toBeFocused();
  }
  if (testInfo.project.name === "mobile-390") {
    const menu = page.getByRole("button", { name: "Toggle navigation" });
    await menu.click();
    await expect(menu).toHaveAttribute("aria-expanded", "true");
    await page.getByRole("link", { name: "Repositories" }).click();
    await expect(menu).toHaveAttribute("aria-expanded", "false");
  }
  await page.evaluate(() => (document.activeElement as HTMLElement | null)?.blur());
  await expect(page).toHaveScreenshot("dashboard.png", { fullPage: true, animations: "disabled" });
});

test("200 percent equivalent reflow has no horizontal clipping", async ({ page }, testInfo) => {
  test.skip(testInfo.project.name !== "desktop-1440");
  await page.setViewportSize({ width: 720, height: 900 });
  await page.goto("/");
  await expect(page.getByRole("heading", { name: "Good work starts with orientation" })).toBeVisible();
  const overflow = await page.evaluate(() => document.documentElement.scrollWidth - document.documentElement.clientWidth);
  expect(overflow).toBeLessThanOrEqual(1);
  await expect(page).toHaveScreenshot("dashboard-200-percent.png", { fullPage: true, animations: "disabled" });
});
