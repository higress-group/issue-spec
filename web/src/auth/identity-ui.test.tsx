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
import { ProfileOnboardingDialog } from "./profile-onboarding-dialog";
import { VerifyEmailPage } from "./verify-email-page";

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

  it("requires name and email in a non-dismissible first-login dialog", async () => {
    let completed = false;
    let nicknameBody: unknown;
    let emailBody: unknown;
    document.cookie = "issue_spec_csrf=onboarding-csrf; Path=/";
    server.use(
      http.get("http://localhost/api/v1/profile", () => HttpResponse.json({
        id: 101, login: "alice", display_name: "Alice", identity_display_name: "Provider Alice",
        nickname: null, representation_version: completed ? 3 : 1, avatar_url: "", html_url: "", type: "User", site_admin: true,
        notification_email_available: true, onboarding_completed: completed, notification_email: null,
        notification_email_verified_at: null, pending_notification_email: null,
      })),
      http.patch("http://localhost/api/v1/profile", async ({ request }) => {
        expect(request.headers.get("X-CSRF-Token")).toBe("onboarding-csrf");
        nicknameBody = await request.json();
        return HttpResponse.json({ id: 101, login: "alice", display_name: "Alice Zhang", identity_display_name: "Provider Alice",
          nickname: "Alice Zhang", representation_version: 2, avatar_url: "", html_url: "", type: "User", site_admin: true,
          notification_email_available: true, onboarding_completed: false, notification_email: null,
          notification_email_verified_at: null, pending_notification_email: null });
      }),
      http.put("http://localhost/api/v1/profile/email", async ({ request }) => {
        expect(request.headers.get("X-CSRF-Token")).toBe("onboarding-csrf");
        emailBody = await request.json();
        completed = true;
        return HttpResponse.json({ id: "dddddddd-dddd-4ddd-8ddd-dddddddddddd", email: "alice.notify@example.test",
          expires_at: "2030-01-01T00:00:00Z", sent_at: null, representation_version: 1 });
      }),
    );
    const { container } = renderApp(<ProfileOnboardingDialog enabled />);
    const dialog = await screen.findByRole("dialog", { name: "How should people find you?" });
    expect(screen.queryByRole("button", { name: /skip|close/i })).not.toBeInTheDocument();
    const name = screen.getByRole("textbox", { name: /Your name/ });
    await userEvent.setup().clear(name);
    await userEvent.setup().type(name, "Alice Zhang");
    await userEvent.setup().type(screen.getByRole("textbox", { name: /Notification email/ }), "alice.notify@example.test");
    await userEvent.setup().click(screen.getByRole("button", { name: "Save and continue" }));
    await waitFor(() => expect(emailBody).toEqual({ email: "alice.notify@example.test", expected_version: 2 }));
    expect(nicknameBody).toEqual({ nickname: "Alice Zhang", expected_version: 1 });
    await waitFor(() => expect(dialog).not.toBeInTheDocument());
    expect((await axe.run(container)).violations).toEqual([]);
  });

  it("keeps fragment verification read-only until explicit confirmation", async () => {
    let inspections = 0;
    let confirmations = 0;
    document.cookie = "issue_spec_csrf=verification-csrf; Path=/";
    server.use(
      http.get("http://localhost/api/v1/profile/email/verification", ({ request }) => {
        inspections += 1;
        expect(new URL(request.url).searchParams.get("token")).toBe("fragment-secret");
        return HttpResponse.json({ status: "ready", expires_at: "2030-01-01T00:00:00Z", representation_version: 1 });
      }),
      http.post("http://localhost/api/v1/profile/email/verification", async ({ request }) => {
        confirmations += 1;
        expect(request.headers.get("X-CSRF-Token")).toBe("verification-csrf");
        expect(await request.json()).toEqual({ token: "fragment-secret" });
        return HttpResponse.json({ status: "confirmed", notification_email: "alice@example.test",
          notification_email_verified_at: "2026-07-18T08:00:00Z", representation_version: 4 });
      }),
    );
    renderApp(<VerifyEmailPage />, "/verify-email#token=fragment-secret");
    expect(await screen.findByRole("button", { name: "Confirm email" })).toBeVisible();
    expect(inspections).toBe(1);
    expect(confirmations).toBe(0);
    await userEvent.setup().click(screen.getByRole("button", { name: "Confirm email" }));
    expect(await screen.findByRole("heading", { name: "Email confirmed" })).toBeVisible();
    expect(confirmations).toBe(1);
  });

  it("shows pending account email controls only when the capability is available", async () => {
    let resendBody: unknown;
    server.use(
      http.get("http://localhost/api/v1/profile", () => HttpResponse.json({
        id: 101, login: "alice", display_name: "Alice", identity_display_name: "Alice", nickname: null,
        representation_version: 4, avatar_url: "", html_url: "", type: "User", site_admin: true,
        notification_email_available: true, onboarding_completed: true, notification_email: null,
        notification_email_verified_at: null, pending_notification_email: { id: "dddddddd-dddd-4ddd-8ddd-dddddddddddd",
          email: "pending@example.test", expires_at: "2030-01-01T00:00:00Z", sent_at: null, representation_version: 2 },
      })),
      http.post("http://localhost/api/v1/profile/email/verification/resend", async ({ request }) => {
        resendBody = await request.json();
        return HttpResponse.json({ id: "eeeeeeee-eeee-4eee-8eee-eeeeeeeeeeee", email: "pending@example.test",
          expires_at: "2030-01-02T00:00:00Z", sent_at: null, representation_version: 1 });
      }),
    );
    renderApp(<AccountPage />, "/settings/account");
    expect(await screen.findByTestId("pending-notification-email")).toHaveTextContent("pending@example.test");
    await userEvent.setup().click(screen.getByTestId("notification-email-resend"));
    await waitFor(() => expect(resendBody).toEqual({ expected_version: 4, expected_verification_version: 2 }));
  });
});
