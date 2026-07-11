import { afterEach, describe, expect, it, vi } from "vitest";
import { apiRequest } from "../src/lib/api/client";

describe("apiRequest", () => {
  afterEach(() => vi.unstubAllGlobals());

  it("keeps requests same-origin, refuses redirects, and sends CSRF only on mutations", async () => {
    document.cookie = "issue_spec_csrf=csrf-value; Path=/";
    const fetchMock = vi.fn().mockImplementation(() => Promise.resolve(new Response(JSON.stringify({ ok: true }), {
      status: 200,
      headers: { "Content-Type": "application/json" },
    })));
    vi.stubGlobal("fetch", fetchMock);
    await apiRequest("/api/v1/read");
    await apiRequest("/api/v1/write", { method: "POST", body: { value: 1 } });
    const [readTarget, readOptions] = fetchMock.mock.calls[0] as [URL, RequestInit];
    const [writeTarget, writeOptions] = fetchMock.mock.calls[1] as [URL, RequestInit];
    expect(readTarget.origin).toBe(window.location.origin);
    expect(writeTarget.origin).toBe(window.location.origin);
    expect(readOptions.redirect).toBe("error");
    expect(readOptions.credentials).toBe("same-origin");
    expect(new Headers(readOptions.headers).has("X-CSRF-Token")).toBe(false);
    expect(new Headers(writeOptions.headers).get("X-CSRF-Token")).toBe("csrf-value");
  });

  it("rejects protocol-relative and absolute network targets before fetch", async () => {
    const fetchMock = vi.fn();
    vi.stubGlobal("fetch", fetchMock);
    await expect(apiRequest("//evil.example/api")).rejects.toThrow("same-origin");
    await expect(apiRequest("https://evil.example/api")).rejects.toThrow("same-origin");
    expect(fetchMock).not.toHaveBeenCalled();
  });
});
