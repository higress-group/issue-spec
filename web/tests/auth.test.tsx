import axe from "axe-core";
import { http, HttpResponse } from "msw";
import { fireEvent, screen, waitFor } from "@testing-library/react";
import userEvent from "@testing-library/user-event";
import { Route, Routes } from "react-router-dom";
import { describe, expect, it } from "vitest";
import { LoginPage } from "../src/auth/login-page";
import { BootstrapPage } from "../src/auth/bootstrap-page";
import { TokensPage } from "../src/auth/tokens-page";
import { renderApp } from "./render";
import { fixtureMeta, server } from "./server";

describe("authentication flows", () => {
  it("discovers providers and renders an accessible login desk", async () => {
    const { container } = renderApp(<LoginPage />, "/login");
    const link = await screen.findByRole("link", { name: /company-oidc/i });
    expect(link).toHaveAttribute("href", expect.stringContaining("/api/v1/auth/company-oidc/login"));
    const result = await axe.run(container);
    expect(result.violations).toEqual([]);
  });

  it("recognizes github-oauth providers and reports trusted HTTP before redirect", async () => {
    server.use(
      http.get("http://localhost/api/v1/auth/providers", () => HttpResponse.json({ providers: [{ name: "github-company", kind: "github-oauth" }] })),
      http.get("http://localhost/api/v1/meta", () => HttpResponse.json({ ...fixtureMeta, transport_posture: "trusted-internal-http" })),
    );
    const { container } = renderApp(<LoginPage />, "/login");
    const link = await screen.findByRole("link", { name: /github-company/i });
    expect(link.querySelector("svg")?.getAttribute("class")).toContain("lucide-git-pull-request");
    expect(screen.getByRole("note")).toHaveTextContent("Trusted internal HTTP");
    expect((await axe.run(container)).violations).toEqual([]);
  });

  it("claims bootstrap and exchanges recovery without browser persistence", async () => {
    let claimBody: any;
    let exchangeBody: any;
    server.use(
      http.post("http://localhost/api/v1/bootstrap/claim", async ({ request }) => {
        claimBody = await request.json();
        return HttpResponse.json({ recovery: { token: "iss_rcv_once_secret", expires_at: "2030-01-01T00:00:00Z" } }, { status: 201 });
      }),
      http.post("http://localhost/api/v1/session/recovery", async ({ request }) => {
        exchangeBody = await request.json();
        return new HttpResponse(null, { status: 204 });
      }),
    );
    renderApp(<Routes><Route path="/bootstrap" element={<BootstrapPage />} /><Route path="/admin" element={<h1>Administration ready</h1>} /></Routes>, "/bootstrap");
    const user = userEvent.setup();
    await user.type(await screen.findByLabelText("Bootstrap secret"), "operator-secret");
    await user.type(screen.getByRole("textbox", { name: /Local login/i }), "alice");
    await user.type(screen.getByRole("textbox", { name: /Display name/i }), "Alice");
    await user.click(screen.getByRole("button", { name: /claim and take over/i }));
    expect(await screen.findByRole("heading", { name: "Administration ready" })).toBeInTheDocument();
    expect(claimBody.secret).toBe("operator-secret");
    expect(exchangeBody).toEqual({ token: "iss_rcv_once_secret" });
    expect(JSON.stringify(localStorage)).not.toContain("iss_rcv_once_secret");
  });

  it("keeps a created PAT only in the show-once dialog", async () => {
    server.use(http.post("http://localhost/api/v1/pats", () => HttpResponse.json({ id: "cccccccc-cccc-4ccc-8ccc-cccccccccccc", name: "runner", token: "iss_pat_once_secret", scopes: ["issues:read"] }, { status: 201 })));
    renderApp(<TokensPage />, "/settings/tokens");
    const user = userEvent.setup();
    await user.type(await screen.findByLabelText("Token name"), "runner");
    await user.click(screen.getByRole("button", { name: /create token/i }));
    expect(await screen.findByRole("dialog", { name: /save this access token/i })).toHaveTextContent("iss_pat_once_secret");
    expect(JSON.stringify(localStorage)).not.toContain("iss_pat_once_secret");
    fireEvent.click(screen.getByRole("button", { name: /i saved it/i }));
    await waitFor(() => expect(screen.queryByText("iss_pat_once_secret")).not.toBeInTheDocument());
  });
});
