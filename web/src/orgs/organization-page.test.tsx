import { http, HttpResponse } from "msw";
import { screen } from "@testing-library/react";
import { Route, Routes } from "react-router-dom";
import { describe, expect, it } from "vitest";
import { renderApp } from "../../tests/render";
import { fixtureContext, fixtureMeta, server } from "../../tests/server";
import { OrganizationPage } from "./organization-page";

const orgId = "bbbbbbbb-bbbb-4bbb-8bbb-bbbbbbbbbbbb";

describe("organization administration navigation", () => {
  it("shows the organization webhook workspace only when mounted and authorized", async () => {
    server.use(
      http.get(`http://localhost/api/v1/orgs/${orgId}`, () => HttpResponse.json({ id: orgId, name: "acme", display_name: "Acme", description: "", base_permission: "read", representation_version: 1 })),
      http.get("http://localhost/api/v1/meta", () => HttpResponse.json({ ...fixtureMeta, features: { ...fixtureMeta.features, webhooks: true } })),
      http.get("http://localhost/api/v1/context", () => HttpResponse.json({ ...fixtureContext, organizations: [{ ...fixtureContext.organizations[0], allowed_actions: [...fixtureContext.organizations[0].allowed_actions, "integrations.manage"] }] })),
    );
    renderApp(<Routes><Route path="/admin/orgs/:orgId" element={<OrganizationPage />} /></Routes>, `/admin/orgs/${orgId}`);
    expect(await screen.findByRole("link", { name: "Webhooks" })).toHaveAttribute("href", `/admin/orgs/${orgId}/integrations/webhooks`);
  });

  it("hides the organization webhook link without integration authority", async () => {
    server.use(
      http.get(`http://localhost/api/v1/orgs/${orgId}`, () => HttpResponse.json({ id: orgId, name: "acme", display_name: "Acme", description: "", base_permission: "read", representation_version: 1 })),
      http.get("http://localhost/api/v1/meta", () => HttpResponse.json({ ...fixtureMeta, features: { ...fixtureMeta.features, webhooks: true } })),
    );
    renderApp(<Routes><Route path="/admin/orgs/:orgId" element={<OrganizationPage />} /></Routes>, `/admin/orgs/${orgId}`);
    expect(await screen.findByRole("heading", { name: "Acme" })).toBeVisible();
    expect(screen.queryByRole("link", { name: "Webhooks" })).not.toBeInTheDocument();
  });
});
