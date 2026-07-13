import { lazy, Suspense, type ReactNode } from "react";
import { CircleDot } from "lucide-react";
import type { FeatureContribution } from "../../app/feature-contributions";
import "./issues.css";

const IssueWorkspacePage = lazy(() => import("./workspace-page").then((module) => ({ default: module.IssueWorkspacePage })));
const IssueListPage = lazy(() => import("./list-page").then((module) => ({ default: module.IssueListPage })));
const IssueCreatePage = lazy(() => import("./create-page").then((module) => ({ default: module.IssueCreatePage })));
const IssueDetailPage = lazy(() => import("./detail-page").then((module) => ({ default: module.IssueDetailPage })));

function LazyIssueRoute({ children }: { children: ReactNode }) {
  return <Suspense fallback={<div className="issue-status" role="status"><span className="issue-loader" aria-hidden="true" /><p>Opening issue desk…</p></div>}>{children}</Suspense>;
}

const contribution: FeatureContribution = {
  navigation: [{ label: "Issues", labelKey: "navigation.issues", to: "/issues", icon: CircleDot, order: 10 }],
  routes: [
    { path: "issues", element: <LazyIssueRoute><IssueWorkspacePage /></LazyIssueRoute> },
    { path: "issues/:orgId/:repoId", element: <LazyIssueRoute><IssueListPage /></LazyIssueRoute> },
    { path: "issues/:orgId/:repoId/new", element: <LazyIssueRoute><IssueCreatePage /></LazyIssueRoute> },
    { path: "issues/:orgId/:repoId/:number", element: <LazyIssueRoute><IssueDetailPage /></LazyIssueRoute> },
    { path: ":owner/:repo/issues", element: <LazyIssueRoute><IssueListPage /></LazyIssueRoute> },
    { path: ":owner/:repo/issues/new", element: <LazyIssueRoute><IssueCreatePage /></LazyIssueRoute> },
    { path: ":owner/:repo/issues/:number", element: <LazyIssueRoute><IssueDetailPage /></LazyIssueRoute> },
  ],
};

export default contribution;
