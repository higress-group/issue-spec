import { describe, expect, it } from "vitest";
import { contextSchema } from "./types";

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
