import axe from "axe-core";
import { http, HttpResponse } from "msw";
import { fireEvent, screen, waitFor } from "@testing-library/react";
import userEvent from "@testing-library/user-event";
import { Route, Routes } from "react-router-dom";
import { describe, expect, it } from "vitest";
import { AdminPage } from "../admin/dashboard-page";
import { renderApp } from "../../tests/render";
import { fixtureContext, fixtureMeta, server } from "../../tests/server";
import { AccountPage } from "./account-page";
import { AuthCompletePage } from "./auth-complete-page";

describe("identity and trusted transport UI", () => {
  it("shows trusted HTTP posture clearly to authenticated administrators", async () => {
    server.use(http.get("http://localhost/api/v1/meta", () => HttpResponse.json({ ...fixtureMeta,
      api_url: "http://10.0.0.8/api/v3", web_url: "http://issues.internal", transport_posture: "trusted-internal-http",
      transport: { mode: "trusted-internal-http", secure: false },
    })));
    const { container } = renderApp(<AdminPage />, "/admin");
    expect(await screen.findByRole("heading", { name: "Trusted internal HTTP" })).toBeVisible();
    expect(screen.getByText("http://10.0.0.8/api/v3")).toBeVisible();
    expect(screen.getByText(/without Secure/)).toBeVisible();
    expect((await axe.run(container)).violations).toEqual([]);
  });

  it("rotates, refreshes and logs out a trusted HTTP browser session", async () => {
    let rotations = 0;
    let logouts = 0;
    let contexts = 0;
    document.cookie = "issue_spec_csrf=trusted-http-csrf; Path=/";
    server.use(
      http.get("http://localhost/api/v1/context", () => { contexts += 1; return HttpResponse.json({ ...fixtureContext, user: { ...fixtureContext.user, avatar_url: "http://localhost/api/v1/avatars/alice" } }); }),
      http.post("http://localhost/api/v1/session/rotate", ({ request }) => { rotations += 1; expect(request.headers.get("X-CSRF-Token")).toBe("trusted-http-csrf"); return HttpResponse.json({ csrf_token: "rotated" }); }),
      http.delete("http://localhost/api/v1/session", () => { logouts += 1; return new HttpResponse(null, { status: 204 }); }),
    );
    renderApp(<Routes><Route path="/settings/account" element={<AccountPage />} /><Route path="/login" element={<h1>Signed out</h1>} /></Routes>, "/settings/account");
    expect(await screen.findByRole("img", { name: "Alice avatar" })).toBeVisible();
    await userEvent.setup().click(screen.getByRole("button", { name: "Rotate session" }));
    await waitFor(() => expect(rotations).toBe(1));
    await waitFor(() => expect(contexts).toBeGreaterThan(1));
    await userEvent.setup().click(screen.getByRole("button", { name: "Sign out" }));
    expect(await screen.findByRole("heading", { name: "Signed out" })).toBeVisible();
    expect(logouts).toBe(1);
  });

  it("saves a preferred nickname and keeps the stable login visible", async () => {
    let submitted: unknown;
    document.cookie = "issue_spec_csrf=profile-csrf; Path=/";
    server.use(http.patch("http://localhost/api/v1/profile", async ({ request }) => {
      expect(request.headers.get("X-CSRF-Token")).toBe("profile-csrf");
      submitted = await request.json();
      return HttpResponse.json({ id: 101, login: "alice", display_name: "澄潭", identity_display_name: "Alice",
        nickname: "澄潭", representation_version: 2, avatar_url: "http://localhost/api/v1/avatars/alice",
        html_url: "http://localhost/users/alice", type: "User", site_admin: true });
    }));
    renderApp(<AccountPage />, "/settings/account");
    const nickname = await screen.findByRole("textbox", { name: /Nickname/ });
    fireEvent.change(nickname, { target: { value: "澄潭" } });
    await userEvent.setup().click(screen.getByRole("button", { name: "Save nickname" }));
    await waitFor(() => expect(submitted).toEqual({ nickname: "澄潭", expected_version: 1 }));
    expect(screen.getByText("@alice")).toBeVisible();
    expect(screen.getByDisplayValue("澄潭")).toBeVisible();
  });

  it("refreshes context after the callback landing page", async () => {
    let contexts = 0;
    server.use(http.get("http://localhost/api/v1/context", () => { contexts += 1; return HttpResponse.json(fixtureContext); }));
    renderApp(<Routes><Route path="/auth/complete" element={<AuthCompletePage />} /><Route path="/" element={<h1>Workspace ready</h1>} /></Routes>, "/auth/complete");
    expect(await screen.findByRole("heading", { name: "Workspace ready" })).toBeVisible();
    expect(contexts).toBeGreaterThan(0);
  });
});
