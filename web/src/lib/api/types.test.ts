import { describe, expect, it } from "vitest";
import { contextSchema, sourceBindingSchema, webhookDeliveryDetailSchema, webhookSecretSchema } from "./types";

const baseContext = {
  user: {
    id: "97fea88c-8558-48f6-b67d-b6c61c8e861c",
    login: "browser-admin",
    display_name: "Browser Administrator",
    site_admin: true,
  },
  credential: {
    kind: "session",
    scope_mode: "identity" as const,
    repository_restricted: false,
  },
  allowed_actions: ["site.admin"],
  organizations: [],
};

describe("contextSchema", () => {
  it("accepts RFC 3339 session expiries with an explicit offset", () => {
    expect(contextSchema.parse({
      ...baseContext,
      credential: {
        ...baseContext.credential,
        absolute_expires_at: "2026-07-18T19:34:44.89664+08:00",
        idle_expires_at: "2026-07-12T07:34:44.89664+08:00",
      },
    }).credential.absolute_expires_at).toBe("2026-07-18T19:34:44.89664+08:00");
  });

  it("still rejects non-RFC 3339 expiry values", () => {
    expect(() => contextSchema.parse({
      ...baseContext,
      credential: { ...baseContext.credential, absolute_expires_at: "next week" },
    })).toThrow();
  });
});

describe("integration API schemas", () => {
  it("accepts the native binding and show-once webhook contracts", () => {
    expect(sourceBindingSchema.parse({ id: "11111111-1111-4111-8111-111111111111", provider_key: "github", external_repository_id: "higress-group/issue-spec", clone_url: "https://github.com/higress-group/issue-spec.git", web_url: "https://github.com/higress-group/issue-spec", default_branch: "main", version: 1, active: true, created_at: "2026-07-11T10:00:00Z", updated_at: "2026-07-11T10:00:00Z" }).version).toBe(1);
    expect(webhookSecretSchema.parse({ id: "22222222-2222-4222-8222-222222222222", organization_id: "bbbbbbbb-bbbb-4bbb-8bbb-bbbbbbbbbbbb", repository_id: "cccccccc-cccc-4ccc-8ccc-cccccccccccc", scope_type: "repository", url: "https://runner.example.test/hook", active: true, event_types: ["issue_comment.created"], retry: { max_attempts: 8, initial_backoff: "1s", max_backoff: "5m0s" }, representation_version: 1, created_at: "2026-07-11T10:00:00Z", updated_at: "2026-07-11T10:00:00Z", secret: "shown-once", secret_version: 1 }).secret).toBe("shown-once");
  });

  it("parses delivery scope and attempt response headers exactly as Go emits them", () => {
    const delivery = { id: "eeeeeeee-eeee-4eee-8eee-eeeeeeeeeeee", scope: { OrgID: "bbbbbbbb-bbbb-4bbb-8bbb-bbbbbbbbbbbb", RepoID: "cccccccc-cccc-4ccc-8ccc-cccccccccccc" }, event_id: "ffffffff-ffff-4fff-8fff-ffffffffffff", subscription_id: "22222222-2222-4222-8222-222222222222", state: "retry", next_attempt_at: "2026-07-11T10:05:00Z", representation_version: 2, created_at: "2026-07-11T10:00:00Z", updated_at: "2026-07-11T10:01:00Z", event_type: "issue_comment.created", repository_sequence: 14, secret_version: 1 };
    const parsed = webhookDeliveryDetailSchema.parse({ delivery, attempts: [{ id: "12345678-1234-4234-8234-123456789abc", attempt_number: 1, response_status: 503, response_headers: { "Retry-After": ["2"] }, started_at: "2026-07-11T10:00:00Z", completed_at: "2026-07-11T10:00:01Z" }] });
    expect(parsed.delivery.scope.RepoID).toBe("cccccccc-cccc-4ccc-8ccc-cccccccccccc");
    expect(parsed.attempts[0].response_headers["Retry-After"]).toEqual(["2"]);
  });
});
