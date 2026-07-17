import axe from "axe-core";
import { http, HttpResponse } from "msw";
import { screen, within } from "@testing-library/react";
import userEvent from "@testing-library/user-event";
import { Route, Routes } from "react-router-dom";
import { describe, expect, it } from "vitest";
import { renderApp } from "../../tests/render";
import { server } from "../../tests/server";
import type { AdminRepository } from "../lib/api/types";
import { CollaboratorsPage } from "./collaborators-page";
import { RepositoryHeader, type RepositorySection } from "./repository-header";
import i18n from "../i18n/i18n";

const orgId = "bbbbbbbb-bbbb-4bbb-8bbb-bbbbbbbbbbbb";
const repoId = "cccccccc-cccc-4ccc-8ccc-cccccccccccc";
const repository: AdminRepository = {
  id: repoId,
  organization_id: orgId,
  name: "issue-spec",
  display_name: "Issue Spec",
  description: "Issue-native specifications",
  visibility: "private",
  default_branch: "main",
  contribution_policy: "members",
  representation_version: 1,
};

const routes: Array<{ section: RepositorySection; route: string }> = [
  { section: "settings", route: `/orgs/${orgId}/repos/${repoId}/settings` },
  { section: "collaborators", route: `/orgs/${orgId}/repos/${repoId}/collaborators` },
  { section: "source", route: `/orgs/${orgId}/repos/${repoId}/integrations/source` },
  { section: "webhooks", route: `/orgs/${orgId}/repos/${repoId}/integrations/webhooks` },
];

describe.each(routes)("repository navigation: $section", ({ section, route }) => {
  it("keeps list and settings return paths accessible", async () => {
    const { container } = renderApp(<RepositoryHeader repository={repository} section={section} title="Repository workspace" description="Repository administration" />, route);
    const breadcrumbs = screen.getByRole("navigation", { name: "Repository breadcrumb" });
    expect(within(breadcrumbs).getByRole("link", { name: "Repositories" })).toHaveAttribute("href", `/orgs/${orgId}/repos`);
    if (section === "settings") {
      expect(within(breadcrumbs).getByText("Issue Spec")).toHaveAttribute("aria-current", "page");
    } else {
      expect(within(breadcrumbs).getByRole("link", { name: "Issue Spec" })).toHaveAttribute("href", `/orgs/${orgId}/repos/${repoId}/settings`);
    }
    const sections = screen.getByRole("navigation", { name: "Repository sections" });
    expect(within(sections).getByRole("link", { name: "Settings" })).toHaveAttribute("href", `/orgs/${orgId}/repos/${repoId}/settings`);
    expect(within(sections).getByRole("link", { name: new RegExp(section, "i") })).toHaveAttribute("aria-current", "page");
    expect((await axe.run(container)).violations).toEqual([]);
  });
});

describe("collaborator collection compatibility", () => {
  it("renders the legacy null response as a usable empty state", async () => {
    server.use(
      http.get(`http://localhost/api/v1/orgs/${orgId}/repos/${repoId}`, () => HttpResponse.json(repository)),
      http.get(`http://localhost/api/v1/orgs/${orgId}/repos/${repoId}/collaborators`, () => HttpResponse.json({ collaborators: null })),
      http.get(`http://localhost/api/v1/orgs/${orgId}/user-candidates`, () => HttpResponse.json({ users: [] })),
    );
    const { container } = renderApp(<Routes><Route path="/orgs/:orgId/repos/:repoId/collaborators" element={<CollaboratorsPage />} /></Routes>, `/orgs/${orgId}/repos/${repoId}/collaborators`);
    expect(await screen.findByRole("heading", { name: "No explicit collaborators" })).toBeVisible();
    expect(screen.getByRole("navigation", { name: "Repository breadcrumb" })).toBeVisible();
    expect((await axe.run(container)).violations).toEqual([]);
  });
});

describe("repository navigation localization", () => {
  it("uses the revised Chinese repository terminology", async () => {
    await i18n.changeLanguage("zh-CN");
    renderApp(<RepositoryHeader repository={repository} section="collaborators" title={i18n.t("collaborators.title")} description={i18n.t("collaborators.description")} />, routes[1].route);
    const breadcrumbs = screen.getByRole("navigation", { name: "仓库路径" });
    expect(within(breadcrumbs).getByRole("link", { name: "仓库" })).toBeVisible();
    const sections = screen.getByRole("navigation", { name: "仓库功能" });
    expect(within(sections).getByRole("link", { name: "协作者" })).toHaveAttribute("aria-current", "page");
    expect(screen.getByRole("heading", { name: "仓库协作者" })).toBeVisible();
  });
});

describe("repository email subscription control", () => {
  it("toggles one explicit repository subscription", async () => {
    await i18n.changeLanguage("en");
    let subscribed = false;
    server.use(
      http.get("http://localhost/api/v1/meta", () => HttpResponse.json({
        api_version: "v1", features: {
          bootstrap: true, personal_access_tokens: true, organizations: true, source_bindings: false,
          webhooks: false, change_boards: false, runner: false, recovery_exchange: true,
          email_notifications: true,
        },
      })),
      http.get("http://localhost/api/v1/profile/email", () => HttpResponse.json({
        available: true, notification_email: "reader@example.test",
      })),
      http.get(`http://localhost/api/v1/orgs/${orgId}/repos/${repoId}/subscription`, () => HttpResponse.json({
        subscribed, ignored: false, reason: subscribed ? "manual" : "", representation_version: subscribed ? 1 : 0,
        collection_version: subscribed ? 2 : 1,
      })),
      http.put(`http://localhost/api/v1/orgs/${orgId}/repos/${repoId}/subscription`, () => {
        subscribed = true;
        return HttpResponse.json({ subscribed: true, ignored: false, reason: "manual", representation_version: 1, collection_version: 2 });
      }),
      http.delete(`http://localhost/api/v1/orgs/${orgId}/repos/${repoId}/subscription`, () => {
        subscribed = false;
        return new HttpResponse(null, { status: 204 });
      }),
    );
    renderApp(<RepositoryHeader repository={repository} section="settings" title="Repository workspace" description="Repository administration" />, routes[0].route);
    const user = userEvent.setup();
    const subscribe = await screen.findByRole("button", { name: "Subscribe" });
    expect(subscribe).toHaveAttribute("aria-pressed", "false");
    await user.click(subscribe);
    const active = await screen.findByRole("button", { name: "Subscribed" });
    expect(active).toHaveAttribute("aria-pressed", "true");
    await user.click(active);
    expect(await screen.findByRole("button", { name: "Subscribe" })).toHaveAttribute("aria-pressed", "false");
  });

  it("guides a user without a verified address to account settings", async () => {
    await i18n.changeLanguage("en");
    server.use(
      http.get("http://localhost/api/v1/meta", () => HttpResponse.json({
        api_version: "v1", features: {
          bootstrap: true, personal_access_tokens: true, organizations: true, source_bindings: false,
          webhooks: false, change_boards: false, runner: false, recovery_exchange: true,
          email_notifications: true,
        },
      })),
      http.get("http://localhost/api/v1/profile/email", () => HttpResponse.json({ available: true, notification_email: null })),
      http.get(`http://localhost/api/v1/orgs/${orgId}/repos/${repoId}/subscription`, () => HttpResponse.json({
        subscribed: false, ignored: false, reason: "", representation_version: 0, collection_version: 1,
      })),
    );
    renderApp(<RepositoryHeader repository={repository} section="settings" title="Repository workspace" description="Repository administration" />, routes[0].route);
    expect(await screen.findByRole("link", { name: "Set notification email" })).toHaveAttribute("href", "/settings/account");
    expect(screen.queryByRole("button", { name: "Subscribe" })).not.toBeInTheDocument();
  });
});
