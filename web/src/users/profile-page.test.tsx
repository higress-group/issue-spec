import { http, HttpResponse } from "msw";
import { screen } from "@testing-library/react";
import { Route, Routes, useLocation } from "react-router-dom";
import { describe, expect, it } from "vitest";
import { renderApp } from "../../tests/render";
import { server } from "../../tests/server";
import { LegacyUserIssuesRedirect, ProfilePage } from "./profile-page";

describe("user profiles", () => {
  it("shows the preferred name and stable login", async () => {
    server.use(http.get("http://localhost/api/v1/users/johnlanni", () => HttpResponse.json({
      id: 101, login: "johnlanni", display_name: "澄潭", avatar_url: "http://localhost/api/v1/avatars/johnlanni",
      html_url: "http://localhost/users/johnlanni", type: "User", site_admin: false,
    })));
    renderApp(<Routes><Route path="/users/:login" element={<ProfilePage />} /></Routes>, "/users/johnlanni");
    expect(await screen.findByRole("heading", { name: "澄潭", level: 1 })).toBeVisible();
    expect(screen.getByText("@johnlanni")).toBeVisible();
  });

  it("redirects the legacy user issues URL to the profile", async () => {
    renderApp(<Routes><Route path="/users/:login/issues" element={<LegacyUserIssuesRedirect />} /><Route path="/users/:login" element={<LocationProbe />} /></Routes>, "/users/johnlanni/issues");
    expect(await screen.findByTestId("location")).toHaveTextContent("/users/johnlanni");
  });
});

function LocationProbe() {
  const location = useLocation();
  return <output data-testid="location">{location.pathname}</output>;
}
