import { http, HttpResponse } from "msw";
import { screen } from "@testing-library/react";
import { Route, Routes } from "react-router-dom";
import { describe, expect, it } from "vitest";
import { renderApp } from "../../tests/render";
import { server } from "../../tests/server";
import { ServiceAccountsPage } from "./service-accounts-page";

const orgId = "bbbbbbbb-bbbb-4bbb-8bbb-bbbbbbbbbbbb";

describe("service account response compatibility", () => {
  it("renders an empty account list when the server returns null", async () => {
    server.use(http.get(`http://localhost/api/v1/orgs/${orgId}/service-accounts`, () => HttpResponse.json({ service_accounts: null })));
    renderApp(<Routes><Route path="/admin/orgs/:orgId/service-accounts" element={<ServiceAccountsPage />} /></Routes>, `/admin/orgs/${orgId}/service-accounts`);
    expect(await screen.findByRole("heading", { name: "Service accounts" })).toBeVisible();
    expect(screen.queryByRole("alert")).not.toBeInTheDocument();
  });
});
