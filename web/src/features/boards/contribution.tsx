import { lazy, Suspense, type ReactNode } from "react";
import { Workflow } from "lucide-react";
import { useTranslation } from "react-i18next";
import type { FeatureContribution } from "../../app/feature-contributions";
import { isChangeFeaturePath } from "../../lib/canonical-routes";
import { LegacyOrganizationChangeRedirect, LegacyRepositoryRedirect } from "../issues/repository-context";
import "./boards.css";

const BoardWorkspacePage = lazy(() => import("./board-page").then((module) => ({ default: module.BoardWorkspacePage })));
const BoardListPage = lazy(() => import("./board-page").then((module) => ({ default: module.BoardListPage })));
const BoardDetailPage = lazy(() => import("./detail-page").then((module) => ({ default: module.BoardDetailPage })));
const RepositoryBoardPage = lazy(() => import("./board-page").then((module) => ({ default: module.RepositoryBoardPage })));

function LazyBoardRoute({ children }: { children: ReactNode }) {
  const { t } = useTranslation();
  return <Suspense fallback={<div className="board-state state-loading" role="status"><span className="board-loader" aria-hidden="true" /><div>{t("changes.openingControl")}</div></div>}>{children}</Suspense>;
}

const contribution: FeatureContribution = {
  navigation: [{ label: "Changes", labelKey: "navigation.changes", to: "/changes", capability: "change_boards", icon: Workflow, order: 20, matches: isChangeFeaturePath }],
  routes: [
    { path: "changes", element: <LazyBoardRoute><BoardWorkspacePage /></LazyBoardRoute> },
    { path: "changes/:orgId", element: <LazyBoardRoute><LegacyOrganizationChangeRedirect /></LazyBoardRoute> },
    { path: "changes/:orgId/repos/:repoId", element: <LazyBoardRoute><LegacyRepositoryRedirect destination="changes" /></LazyBoardRoute> },
    { path: "changes/:orgId/repos/:repoId/:change", element: <LazyBoardRoute><LegacyRepositoryRedirect destination="change-detail" /></LazyBoardRoute> },
    { path: "orgs/:owner/changes", element: <LazyBoardRoute><BoardListPage /></LazyBoardRoute> },
    { path: ":owner/:repo/changes", element: <LazyBoardRoute><RepositoryBoardPage /></LazyBoardRoute> },
    { path: ":owner/:repo/changes/:change", element: <LazyBoardRoute><BoardDetailPage /></LazyBoardRoute> },
  ],
};

export default contribution;
