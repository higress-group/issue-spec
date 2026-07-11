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
import { OrganizationPage } from "../orgs/organization-page";
import { MembersPage } from "../orgs/members-page";
import { RepositoriesPage } from "../repos/repositories-page";
import { RepositorySettingsPage } from "../repos/repository-settings-page";
import { CollaboratorsPage } from "../repos/collaborators-page";
import { IntegrationsPage } from "../repos/integrations-page";

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
  const error = useRouteError();
  return <div className="public-narrow"><div className="empty-state"><span className="eyebrow coral-text">Route error</span><h1>That desk could not open</h1><p>{error instanceof Error ? error.message : "The requested route failed."}</p><Link className="button primary" to="/">Return to overview</Link></div></div>;
}

function NotFoundPage() {
  return <div className="page"><div className="empty-state"><span className="eyebrow">404 / not found</span><h1>No workflow lives here</h1><p>The path may be stale, or the resource is concealed by your current authority.</p><Link className="button primary" to="/">Return to overview</Link></div></div>;
}
