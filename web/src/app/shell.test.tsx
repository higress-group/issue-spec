import axe from "axe-core";
import { QueryClient, QueryClientProvider } from "@tanstack/react-query";
import { http, HttpResponse } from "msw";
import { render, screen, within } from "@testing-library/react";
import { createMemoryRouter, RouterProvider } from "react-router-dom";
import { describe, expect, it } from "vitest";
import { fixtureMeta, server } from "../../tests/server";
import { InspectorProvider } from "./problem-inspector";
import { AuthenticatedShell } from "./shell";

describe("application navigation and canonical public shell", () => {
  it("orders Issues and Changes before Repositories with distinct feature icons on desktop and mobile", async () => {
    server.use(http.get("http://localhost/api/v1/meta", () => HttpResponse.json({ ...fixtureMeta, features: { ...fixtureMeta.features, change_boards: true } })));
    const { container } = renderShell("/");
    const primary = await screen.findByRole("navigation", { name: "Primary navigation" });
    const workspace = within(primary).getAllByRole("link").map((link) => link.textContent?.trim()).filter((label) => ["Overview", "Issues", "Changes", "Repositories"].includes(label ?? ""));
    expect(workspace).toEqual(["Overview", "Issues", "Changes", "Repositories"]);
    const issues = within(primary).getByRole("link", { name: "Issues" });
    const changes = within(primary).getByRole("link", { name: "Changes" });
    expect(issues.querySelector("svg")?.getAttribute("class")).toContain("lucide-circle-dot");
    expect(changes.querySelector("svg")?.getAttribute("class")).toContain("lucide-workflow");
    expect(issues.querySelector("svg")?.getAttribute("class")).not.toBe(changes.querySelector("svg")?.getAttribute("class"));
    const mobile = screen.getByRole("navigation", { name: "Mobile navigation" });
    const mobileLabels = within(mobile).getAllByRole("link").map((link) => link.textContent?.trim());
    expect(mobileLabels).toEqual(["Home", "Issues", "Changes", "Repos", "Account"]);
    expect((await axe.run(container)).violations).toEqual([]);
  });

  it("renders a lightweight public repository shell only for canonical read routes", async () => {
    server.use(
      http.get("http://localhost/api/v1/meta", () => HttpResponse.json({ ...fixtureMeta, features: { ...fixtureMeta.features, change_boards: true } })),
      http.get("http://localhost/api/v1/context", () => HttpResponse.json({ status: 401, title: "Authentication required", code: "authentication_required" }, { status: 401 })),
    );
    const { container } = renderShell("/acme/public/issues/7?view=timeline");
    expect(await screen.findByText("Public issue content")).toBeVisible();
    expect(screen.getByText("public repository view")).toBeVisible();
    expect(screen.getByRole("link", { name: "Sign in" })).toHaveAttribute("href", "/login");
    expect(screen.queryByRole("navigation", { name: "Primary navigation" })).not.toBeInTheDocument();
    expect((await axe.run(container)).violations).toEqual([]);
  });

  it("keeps ordinary protected routes behind login", async () => {
    server.use(http.get("http://localhost/api/v1/context", () => HttpResponse.json({ status: 401, title: "Authentication required", code: "authentication_required" }, { status: 401 })));
    renderShell("/admin");
    expect(await screen.findByText("Login destination")).toBeVisible();
  });
});

function renderShell(initialEntry: string) {
  const client = new QueryClient({ defaultOptions: { queries: { retry: false } } });
  const router = createMemoryRouter([
    { path: "/", element: <AuthenticatedShell />, children: [
      { index: true, element: <div>Dashboard</div> },
      { path: "admin", element: <div>Admin content</div> },
      { path: ":owner/:repo/issues/:number", element: <div>Public issue content</div> },
    ] },
    { path: "/login", element: <div>Login destination</div> },
  ], { initialEntries: [initialEntry] });
  return render(<QueryClientProvider client={client}><InspectorProvider><RouterProvider router={router} /></InspectorProvider></QueryClientProvider>);
}
