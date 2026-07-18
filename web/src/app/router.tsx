import { createBrowserRouter, Link, useRouteError } from "react-router-dom";
import { AuthenticatedShell, PublicShell } from "./shell";
import { featureRoutes } from "./feature-contributions";
import { AdminPage, DashboardPage } from "../admin/dashboard-page";
import { ServiceAccountsPage, ManagedTokensPage } from "../admin/service-accounts-page";
import { LoginPage } from "../auth/login-page";
import { AuthCompletePage } from "../auth/auth-complete-page";
import { BootstrapPage } from "../auth/bootstrap-page";
import { AccountPage } from "../auth/account-page";
import { TokensPage } from "../auth/tokens-page";
import { VerifyEmailPage } from "../auth/verify-email-page";
import { OrganizationPage } from "../orgs/organization-page";
import { MembersPage } from "../orgs/members-page";
import { RepositoriesPage } from "../repos/repositories-page";
import { RepositorySettingsPage } from "../repos/repository-settings-page";
import { CollaboratorsPage } from "../repos/collaborators-page";
import { IntegrationsPage } from "../repos/integrations-page";
import { LegacyUserIssuesRedirect, ProfilePage } from "../users/profile-page";
import { useTranslation } from "react-i18next";

export const router = createBrowserRouter([
  {
    element: <PublicShell />,
    children: [
      { path: "/login", element: <LoginPage /> },
      { path: "/auth/complete", element: <AuthCompletePage /> },
      { path: "/bootstrap", element: <BootstrapPage /> },
    ],
  },
  {
    path: "/",
    element: <AuthenticatedShell />,
    errorElement: <RouteErrorPage />,
    children: [
      { index: true, element: <DashboardPage /> },
      { path: "settings/account", element: <AccountPage /> },
      { path: "settings/tokens", element: <TokensPage /> },
      { path: "verify-email", element: <VerifyEmailPage /> },
      { path: "users/:login", element: <ProfilePage /> },
      { path: "users/:login/issues", element: <LegacyUserIssuesRedirect /> },
      { path: "admin", element: <AdminPage /> },
      { path: "admin/orgs/:orgId", element: <OrganizationPage /> },
      { path: "admin/orgs/:orgId/members", element: <MembersPage /> },
      { path: "admin/orgs/:orgId/service-accounts", element: <ServiceAccountsPage /> },
      { path: "admin/orgs/:orgId/managed-tokens", element: <ManagedTokensPage /> },
      { path: "orgs/:orgId/repos", element: <RepositoriesPage /> },
      { path: "orgs/:orgId/repos/:repoId/settings", element: <RepositorySettingsPage /> },
      { path: "orgs/:orgId/repos/:repoId/collaborators", element: <CollaboratorsPage /> },
      { path: "orgs/:orgId/repos/:repoId/integrations/source", element: <IntegrationsPage kind="source" /> },
      { path: "orgs/:orgId/repos/:repoId/integrations/webhooks", element: <IntegrationsPage kind="webhooks" /> },
      ...featureRoutes,
      { path: "*", element: <NotFoundPage /> },
    ],
  },
]);

function RouteErrorPage() {
  const { t } = useTranslation();
  const error = useRouteError();
  return <div className="public-narrow"><div className="empty-state"><span className="eyebrow coral-text">{t("route.errorEyebrow")}</span><h1>{t("route.errorTitle")}</h1><p>{error instanceof Error ? error.message : t("route.errorFallback")}</p><Link className="button primary" to="/">{t("route.returnRepositories")}</Link></div></div>;
}

function NotFoundPage() {
  const { t } = useTranslation();
  return <div className="page"><div className="empty-state"><span className="eyebrow">{t("route.notFoundEyebrow")}</span><h1>{t("route.notFoundTitle")}</h1><p>{t("route.notFoundDescription")}</p><Link className="button primary" to="/">{t("route.returnRepositories")}</Link></div></div>;
}
