import { lazy, Suspense, type ReactNode } from "react";
import { CircleDot } from "lucide-react";
import { useTranslation } from "react-i18next";
import type { FeatureContribution } from "../../app/feature-contributions";
import { isIssueFeaturePath } from "../../lib/canonical-routes";
import { LegacyRepositoryRedirect, RepositoryRootRedirect } from "./repository-context";
import "./issues.css";

const IssueWorkspacePage = lazy(() => import("./workspace-page").then((module) => ({ default: module.IssueWorkspacePage })));
const IssueListPage = lazy(() => import("./list-page").then((module) => ({ default: module.IssueListPage })));
const IssueCreatePage = lazy(() => import("./create-page").then((module) => ({ default: module.IssueCreatePage })));
const IssueDetailPage = lazy(() => import("./detail-page").then((module) => ({ default: module.IssueDetailPage })));

function LazyIssueRoute({ children }: { children: ReactNode }) {
  const { t } = useTranslation();
  return <Suspense fallback={<div className="issue-status" role="status"><span className="issue-loader" aria-hidden="true" /><p>{t("issues.openingDesk")}</p></div>}>{children}</Suspense>;
}

const contribution: FeatureContribution = {
  navigation: [{ label: "Issues", labelKey: "navigation.issues", to: "/issues", icon: CircleDot, order: 10, matches: isIssueFeaturePath }],
  routes: [
    { path: "issues", element: <LazyIssueRoute><IssueWorkspacePage /></LazyIssueRoute> },
    { path: "issues/:orgId/:repoId", element: <LazyIssueRoute><LegacyRepositoryRedirect destination="issues" /></LazyIssueRoute> },
    { path: "issues/:orgId/:repoId/new", element: <LazyIssueRoute><LegacyRepositoryRedirect destination="issue-new" /></LazyIssueRoute> },
    { path: "issues/:orgId/:repoId/:number", element: <LazyIssueRoute><LegacyRepositoryRedirect destination="issue-detail" /></LazyIssueRoute> },
    { path: ":owner/:repo/issues", element: <LazyIssueRoute><IssueListPage /></LazyIssueRoute> },
    { path: ":owner/:repo/issues/new", element: <LazyIssueRoute><IssueCreatePage /></LazyIssueRoute> },
    { path: ":owner/:repo/issues/:number", element: <LazyIssueRoute><IssueDetailPage /></LazyIssueRoute> },
    { path: ":owner/:repo", element: <LazyIssueRoute><RepositoryRootRedirect /></LazyIssueRoute> },
  ],
};

export default contribution;
