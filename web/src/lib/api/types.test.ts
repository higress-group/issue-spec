import { describe, expect, it } from "vitest";
import { collaboratorsSchema, contextSchema, sourceBindingSchema, webhookDeliveryDetailSchema, webhookSecretSchema, webhookSubscriptionSchema, webhookSuppressionsSchema } from "./types";

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
    expect(webhookSecretSchema.parse({ ...webhookContract, secret: "shown-once", secret_version: 1 }).secret).toBe("shown-once");
  });

  it("distinguishes a terminal webhook from a resumable pause", () => {
    const revokedAt = "2026-07-11T11:00:00Z";
    const parsed = webhookSubscriptionSchema.parse({ ...webhookContract, active: false, revoked_at: revokedAt, representation_version: 2, updated_at: revokedAt });
    expect(parsed.revoked_at).toBe(revokedAt);
    expect(parsed.active).toBe(false);
  });

  it("parses delivery scope and attempt response headers exactly as Go emits them", () => {
    const delivery = { id: "eeeeeeee-eeee-4eee-8eee-eeeeeeeeeeee", scope: { OrgID: "bbbbbbbb-bbbb-4bbb-8bbb-bbbbbbbbbbbb", RepoID: "cccccccc-cccc-4ccc-8ccc-cccccccccccc" }, event_id: "ffffffff-ffff-4fff-8fff-ffffffffffff", subscription_id: "22222222-2222-4222-8222-222222222222", state: "pending", next_attempt_at: "2026-07-11T10:05:00Z", representation_version: 2, created_at: "2026-07-11T10:00:00Z", updated_at: "2026-07-11T10:01:00Z", event_type: "issue_comment.created", delivery_format: "github.v3", event_name: "issue_comment", action: "created", repository_sequence: 14, secret_version: 1 };
    const parsed = webhookDeliveryDetailSchema.parse({ delivery, attempts: [{ id: "12345678-1234-4234-8234-123456789abc", attempt_number: 1, response_status: 503, response_headers: { "Retry-After": ["2"] }, started_at: "2026-07-11T10:00:00Z", completed_at: "2026-07-11T10:00:01Z" }] });
    expect(parsed.delivery.scope.RepoID).toBe("cccccccc-cccc-4ccc-8ccc-cccccccccccc");
    expect(parsed.attempts[0].response_headers["Retry-After"]).toEqual(["2"]);
    expect(() => webhookDeliveryDetailSchema.parse({ delivery: { ...delivery, state: "retry" }, attempts: [] })).toThrow();
    expect(() => webhookDeliveryDetailSchema.parse({ delivery: { ...delivery, state: "delivered" }, attempts: [] })).toThrow();
  });

  it("parses non-secret suppression outcomes", () => {
    const parsed = webhookSuppressionsSchema.parse({ suppressions: [{ id: "99999999-9999-4999-8999-999999999999", organization_id: "bbbbbbbb-bbbb-4bbb-8bbb-bbbbbbbbbbbb", repository_id: "cccccccc-cccc-4ccc-8ccc-cccccccccccc", event_id: "ffffffff-ffff-4fff-8fff-ffffffffffff", subscription_id: "22222222-2222-4222-8222-222222222222", event_type: "issue_comment.created", action: "created", issue_kind: "proposal", comment_class: "typed", actor_class: "human", reason: "comment_class_filtered", created_at: "2026-07-11T10:00:00Z" }] });
    expect(parsed.suppressions[0].reason).toBe("comment_class_filtered");
  });
});

const webhookContract = { id: "22222222-2222-4222-8222-222222222222", organization_id: "bbbbbbbb-bbbb-4bbb-8bbb-bbbbbbbbbbbb", repository_id: "cccccccc-cccc-4ccc-8ccc-cccccccccccc", scope_type: "repository", url: "https://runner.example.test/hook", active: true, event_types: ["issue_comment.created"], delivery_format: "issue-spec.v1", signing_mode: "bearer", content_policy: { issue_actions: ["opened"], comment_actions: ["created"], issue_kinds: ["ordinary"], comment_classes: ["human-untyped"], actor_classes: ["human"] }, has_destination_query: false, retry: { max_attempts: 8, initial_backoff: "1s", max_backoff: "5m0s" }, representation_version: 1, created_at: "2026-07-11T10:00:00Z", updated_at: "2026-07-11T10:00:00Z" };

describe("collaboratorsSchema", () => {
  it("normalizes the legacy null collection to an empty array", () => {
    expect(collaboratorsSchema.parse({ collaborators: null })).toEqual({ collaborators: [] });
    expect(collaboratorsSchema.parse({ collaborators: [] })).toEqual({ collaborators: [] });
  });

  it("rejects malformed collection objects instead of hiding them", () => {
    expect(() => collaboratorsSchema.parse({ collaborators: {} })).toThrow();
    expect(() => collaboratorsSchema.parse({})).toThrow();
  });
});
