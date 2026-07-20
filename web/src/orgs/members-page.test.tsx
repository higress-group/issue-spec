import { http, HttpResponse } from "msw";
import { screen } from "@testing-library/react";
import userEvent from "@testing-library/user-event";
import { Route, Routes } from "react-router-dom";
import { describe, expect, it, vi } from "vitest";
import { renderApp } from "../../tests/render";
import { server } from "../../tests/server";
import { MembersPage } from "./members-page";

const orgId = "bbbbbbbb-bbbb-4bbb-8bbb-bbbbbbbbbbbb";
const candidate = { id: "cccccccc-cccc-4ccc-8ccc-cccccccccccc", login: "a1b2c3d4", display_name: "Alicia", kind: "human", status: "active" };

describe("organization members", () => {
  it("searches by login prefix, selects a suggestion, and adds the selected member", async () => {
    const invite = vi.fn();
    server.use(
      http.get(`http://localhost/api/v1/orgs/${orgId}/memberships`, () => HttpResponse.json({ memberships: [] })),
      http.get(`http://localhost/api/v1/orgs/${orgId}/user-candidates`, ({ request }) => {
        const parameters = new URL(request.url).searchParams;
        if (parameters.get("purpose") === "membership") {
          expect(parameters.get("match")).toBe("prefix");
          expect(parameters.get("query")).toBe("ali");
          expect(parameters.get("limit")).toBe("10");
          return HttpResponse.json({ users: [candidate] });
        }
        return HttpResponse.json({ users: [] });
      }),
      http.post(`http://localhost/api/v1/orgs/${orgId}/memberships`, async ({ request }) => {
        invite(await request.json());
        return HttpResponse.json({ id: "dddddddd-dddd-4ddd-8ddd-dddddddddddd" }, { status: 201 });
      }),
    );
    renderApp(<Routes><Route path="/orgs/:orgId/members" element={<MembersPage />} /></Routes>, `/orgs/${orgId}/members`);
    const user = userEvent.setup();
    const login = await screen.findByRole("textbox", { name: "Local login" });
    await user.type(login, "ali");
    const suggestions = await screen.findByRole("listbox", { name: "User suggestions" });
    expect(login).toHaveAttribute("aria-controls", suggestions.id);
    expect(login).toHaveAttribute("aria-activedescendant");
    expect(suggestions).toHaveTextContent("Alicia");
    expect(suggestions).toHaveTextContent("@a1b2c3d4");
    await user.keyboard("{Enter}");
    expect(login).toHaveValue("a1b2c3d4");
    expect(screen.getByText("Selected Alicia (@a1b2c3d4)")).toBeVisible();
    await user.click(screen.getByRole("button", { name: "Add member" }));
    expect(invite).toHaveBeenCalledWith({ user_id: candidate.id, role: "member" });
  });
});
