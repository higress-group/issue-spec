import AxeBuilder from "@axe-core/playwright";
import { expect, test } from "@playwright/test";
import { fixtureContext, fixtureMeta } from "../server";

test.beforeEach(async ({ page }) => {
  await page.route("**/api/v1/**", async (route) => {
    const request = route.request();
    const url = new URL(request.url());
    if (url.pathname === "/api/v1/meta") return route.fulfill({ json: { ...fixtureMeta,
      transport_posture: "trusted-internal-http", transport: { mode: "trusted-internal-http", secure: false },
      api_url: "http://10.0.0.8/api/v3", web_url: "http://issues.internal",
    } });
    if (url.pathname === "/api/v1/context") return route.fulfill({ json: fixtureContext });
    if (url.pathname === "/api/v1/auth/providers") return route.fulfill({ json: { providers: [{ name: "github-company", kind: "github-oauth" }] } });
    if (url.pathname === "/api/v1/session/rotate" && request.method() === "POST") return route.fulfill({ json: { csrf_token: "rotated" } });
    if (url.pathname === "/api/v1/session" && request.method() === "DELETE") return route.fulfill({ status: 204 });
    if (url.pathname.startsWith("/api/v1/avatars/")) return route.fulfill({ status: 404 });
    return route.fulfill({ status: 404, contentType: "application/problem+json", body: JSON.stringify({ status: 404, title: "Not found", code: "not_found" }) });
  });
});

test("trusted HTTP login and administrator posture remain explicit and responsive", async ({ page }) => {
  await page.goto("/login");
  await expect(page.getByRole("note")).toContainText("Trusted internal HTTP");
  const provider = page.getByRole("link", { name: /github-company/i });
  await expect(provider).toHaveAttribute("href", /\/api\/v1\/auth\/github-company\/login/);
  await expect(provider.locator("svg").first()).toHaveClass(/lucide-git-pull-request/);
  await page.goto("/admin");
  await expect(page.getByRole("heading", { name: "Trusted internal HTTP" })).toBeVisible();
  await expect(page.getByText("http://10.0.0.8/api/v3")).toBeVisible();
  expect(await page.evaluate(() => document.documentElement.scrollWidth - document.documentElement.clientWidth)).toBeLessThanOrEqual(1);
  expect((await new AxeBuilder({ page }).analyze()).violations).toEqual([]);
});

test("unavailable avatars fall back while session rotation, logout and callback refresh remain usable", async ({ page }) => {
  let rotations = 0;
  await page.route("**/api/v1/session/rotate", async (route) => { rotations += 1; return route.fulfill({ json: { csrf_token: "rotated" } }); });
  await page.goto("/settings/account");
  const avatar = page.locator("#main-content").getByRole("img", { name: "Alice avatar" });
  await expect(avatar).toContainText("AL");
  await page.getByRole("button", { name: "Rotate session" }).click();
  await expect.poll(() => rotations).toBe(1);
  await page.getByRole("button", { name: "Sign out" }).click();
  await expect(page).toHaveURL(/\/login$/);
  await page.goto("/auth/complete");
  await expect(page.getByRole("heading", { name: "Repositories" })).toBeVisible();
  expect((await new AxeBuilder({ page }).analyze()).violations).toEqual([]);
});
