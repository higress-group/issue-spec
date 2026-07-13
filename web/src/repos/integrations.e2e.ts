import AxeBuilder from "@axe-core/playwright";
import { expect, test } from "@playwright/test";

const orgId = "bbbbbbbb-bbbb-4bbb-8bbb-bbbbbbbbbbbb";
const repoId = "cccccccc-cccc-4ccc-8ccc-cccccccccccc";
const webhookId = "dddddddd-dddd-4ddd-8ddd-dddddddddddd";
const deliveryId = "eeeeeeee-eeee-4eee-8eee-eeeeeeeeeeee";
const eventId = "ffffffff-ffff-4fff-8fff-ffffffffffff";
const policy = { issue_actions: ["opened"], comment_actions: ["created"], issue_kinds: ["proposal"], comment_classes: ["human-untyped"], actor_classes: ["human"] };
const webhook = { id: webhookId, organization_id: orgId, repository_id: repoId, scope_type: "repository", url: "https://robot.example.test/hook", active: true, event_types: ["issue.created", "issue_comment.created"], delivery_format: "github.v3", signing_mode: "hmac-sha256", content_policy: policy, has_destination_query: true, retry: { max_attempts: 8, initial_backoff: "1s", max_backoff: "5m0s" }, representation_version: 3, created_at: "2026-07-11T09:00:00Z", updated_at: "2026-07-11T09:30:00Z" };
const delivery = { id: deliveryId, scope: { OrgID: orgId, RepoID: repoId }, event_id: eventId, subscription_id: webhookId, state: "dead", next_attempt_at: "2026-07-11T10:05:00Z", last_error: "HTTP 503", representation_version: 2, created_at: "2026-07-11T10:00:00Z", updated_at: "2026-07-11T10:01:00Z", event_type: "issue_comment.created", delivery_format: "github.v3", event_name: "issue_comment", action: "created", repository_sequence: 14, secret_version: 1 };

test.beforeEach(async ({ page }) => {
  await page.route("**/*", async (route) => {
    const request = route.request(); const url = new URL(request.url());
    if (url.pathname === "/api/v1/meta") return route.fulfill({ json: { api_version: "v1", features: { bootstrap: true, personal_access_tokens: true, organizations: true, source_bindings: true, webhooks: true, change_boards: true, runner: true, recovery_exchange: true } } });
    if (url.pathname === "/api/v1/context") return route.fulfill({ json: { user: { id: "aaaaaaaa-aaaa-4aaa-8aaa-aaaaaaaaaaaa", login: "operator", display_name: "Operator", site_admin: true }, credential: { kind: "session", scope_mode: "identity", repository_restricted: false }, allowed_actions: ["site.admin"], organizations: [{ id: orgId, name: "acme", display_name: "Acme", effective_permission: "admin", container_only: false, allowed_actions: ["organization.admin"] }] } });
    if (url.pathname === `/api/v1/context/orgs/${orgId}/repos`) return route.fulfill({ json: { repositories: [{ repository: { id: repoId, organization_id: orgId, name: "workflow", display_name: "Workflow", visibility: "private", contribution_policy: "members" }, effective_permission: "maintain", allowed_actions: ["read", "integrations.manage"] }] } });
    if (url.pathname === `/api/v1/orgs/${orgId}/repos/${repoId}`) return route.fulfill({ json: { id: repoId, organization_id: orgId, name: "workflow", display_name: "Workflow", visibility: "private", default_branch: "main", contribution_policy: "members", representation_version: 1 } });
    if (url.pathname === `/api/v1/orgs/${orgId}/webhooks`) return route.fulfill({ json: { subscriptions: [webhook] } });
    if (url.pathname === `/api/v1/orgs/${orgId}/webhooks/${webhookId}/suppressions`) return route.fulfill({ json: { suppressions: [{ id: "99999999-9999-4999-8999-999999999999", organization_id: orgId, repository_id: repoId, event_id: eventId, subscription_id: webhookId, event_type: "issue_comment.created", action: "created", issue_kind: "proposal", comment_class: "typed", actor_class: "human", reason: "comment_class_filtered", created_at: "2026-07-11T10:00:00Z" }] } });
    if (url.pathname === `/api/v1/orgs/${orgId}/repos/${repoId}/deliveries/${deliveryId}` && request.method() === "GET") return route.fulfill({ json: { delivery, attempts: [{ id: "12345678-1234-4234-8234-123456789abc", attempt_number: 1, response_status: 503, response_headers: { "Retry-After": ["2"] }, started_at: "2026-07-11T10:00:00Z", completed_at: "2026-07-11T10:00:01Z" }] } });
    if (url.pathname === `/api/v1/orgs/${orgId}/repos/${repoId}/deliveries/${deliveryId}/redeliver`) return route.fulfill({ status: 202, json: { ...delivery, state: "pending" } });
    if (url.pathname === `/api/v1/orgs/${orgId}/repos/${repoId}/deliveries`) return route.fulfill({ json: { deliveries: [delivery] } });
    return route.fallback();
  });
});

test("notification control room keeps credentials redacted and replay traceable", async ({ page }, testInfo) => {
  await page.goto(`/orgs/${orgId}/repos/${repoId}/integrations/webhooks`);
  await expect(page.getByRole("heading", { name: "Delivery control room" })).toBeVisible();
  await expect(page.getByText("Encrypted destination credential")).toBeVisible();
  await page.getByRole("button", { name: "Configure" }).click();
  await expect(page.getByRole("textbox", { name: /^Receiver URL/ })).toHaveValue("https://robot.example.test/hook");
  await expect(page.getByText(/encrypted query is intentionally absent/i)).toBeVisible();
  await page.getByRole("button", { name: "Suppressions" }).click();
  await expect(page.getByText(/comment class filtered/i)).toBeVisible();
  await page.getByRole("button", { name: /Issue comment created dead/i }).click();
  await expect(page.getByText("v1 · frozen for replay")).toBeVisible();
  await expect(page.getByRole("button", { name: "Replay immutable delivery" })).toBeEnabled();
  expect((await new AxeBuilder({ page }).analyze()).violations).toEqual([]);
  expect(await page.evaluate(() => document.documentElement.scrollWidth - document.documentElement.clientWidth)).toBeLessThanOrEqual(1);
  if (testInfo.project.name === "integrations-desktop") {
    // Keep the documentation image compact after exercising the expanded
    // configuration and suppression states above.
    await page.getByRole("button", { name: "Configure" }).click();
    await page.getByRole("button", { name: "Suppressions" }).click();
    await page.evaluate(() => {
      document.body.tabIndex = -1;
      document.body.focus();
    });
    await page.locator(".skip-link").evaluate((node) => node.remove());
    await expect(page).toHaveScreenshot("webhook-integrations.png", { fullPage: true, animations: "disabled" });
  }
});
