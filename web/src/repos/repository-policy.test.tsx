import { http, HttpResponse } from "msw";
import { beforeEach, describe, expect, it } from "vitest";
import { screen } from "@testing-library/react";
import userEvent from "@testing-library/user-event";
import { Route, Routes } from "react-router-dom";
import { renderApp } from "../../tests/render";
import { fixtureContext, server } from "../../tests/server";
import i18n from "../i18n/i18n";
import type { AdminRepository } from "../lib/api/types";
import { RepositoriesPage, suggestedContributionPolicy } from "./repositories-page";
import { RepositorySettingsPage } from "./repository-settings-page";

const orgId = "bbbbbbbb-bbbb-4bbb-8bbb-bbbbbbbbbbbb";
const repoId = "cccccccc-cccc-4ccc-8ccc-cccccccccccc";
const repository: AdminRepository = {
  id: repoId,
  organization_id: orgId,
  name: "requirements",
  display_name: "Requirements",
  description: "Public requirements",
  visibility: "private",
  default_branch: "main",
  contribution_policy: "members",
  representation_version: 1,
};

beforeEach(async () => {
  await i18n.changeLanguage("en");
});

describe("repository contribution policy", () => {
  it("suggests public contribution without replacing an explicit policy", () => {
    expect(suggestedContributionPolicy("public", "members", false)).toBe("public");
    expect(suggestedContributionPolicy("private", "public", false)).toBe("members");
    expect(suggestedContributionPolicy("public", "members", true)).toBe("members");
    expect(suggestedContributionPolicy("public", "disabled", true)).toBe("disabled");
  });

  it("applies the suggestion only until the administrator chooses a policy", async () => {
    server.use(
      http.get(`http://localhost/api/v1/context/orgs/${orgId}/repos`, () => HttpResponse.json({ repositories: [] })),
    );
    renderApp(<Routes><Route path="/orgs/:orgId/repos" element={<RepositoriesPage />} /></Routes>, `/orgs/${orgId}/repos`);

    const visibility = await screen.findByRole("combobox", { name: "Visibility" });
    const contribution = screen.getByRole("combobox", { name: "Contribution policy" });
    expect(contribution).toHaveValue("members");

    await userEvent.setup().selectOptions(visibility, "public");
    expect(contribution).toHaveValue("public");
    expect(screen.getByText("Public → Public repository users · Contribute")).toBeVisible();

    await userEvent.setup().selectOptions(contribution, "members");
    await userEvent.setup().selectOptions(visibility, "private");
    await userEvent.setup().selectOptions(visibility, "public");
    expect(contribution).toHaveValue("members");
  });

  it("keeps the stored settings policy authoritative when visibility changes", async () => {
    server.use(
      http.get(`http://localhost/api/v1/orgs/${orgId}/repos/${repoId}`, () => HttpResponse.json(repository)),
    );
    renderApp(<Routes><Route path="/orgs/:orgId/repos/:repoId/settings" element={<RepositorySettingsPage />} /></Routes>, `/orgs/${orgId}/repos/${repoId}/settings`);

    const visibility = await screen.findByRole("combobox", { name: "Visibility" });
    const contribution = screen.getByRole("combobox", { name: "Contribution policy" });
    await userEvent.setup().selectOptions(visibility, "public");
    expect(contribution).toHaveValue("members");
    expect(screen.getByText("Public → Public repository users · Contribute")).toBeVisible();
  });

  it("uses existing localized policy terms for the public-contribution hint", async () => {
    await i18n.changeLanguage("zh-CN");
    server.use(
      http.get(`http://localhost/api/v1/context`, () => HttpResponse.json(fixtureContext)),
      http.get(`http://localhost/api/v1/context/orgs/${orgId}/repos`, () => HttpResponse.json({ repositories: [] })),
    );
    renderApp(<Routes><Route path="/orgs/:orgId/repos" element={<RepositoriesPage />} /></Routes>, `/orgs/${orgId}/repos`);
    expect(await screen.findByText("公开 → 公开仓库用户 · 参与")).toBeVisible();
  });
});
