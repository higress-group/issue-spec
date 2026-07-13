import { lazy, Suspense, type ReactNode } from "react";
import { Workflow } from "lucide-react";
import type { FeatureContribution } from "../../app/feature-contributions";
import "./boards.css";

const BoardWorkspacePage = lazy(() => import("./board-page").then((module) => ({ default: module.BoardWorkspacePage })));
const BoardListPage = lazy(() => import("./board-page").then((module) => ({ default: module.BoardListPage })));
const BoardDetailPage = lazy(() => import("./detail-page").then((module) => ({ default: module.BoardDetailPage })));
const RepositoryBoardPage = lazy(() => import("./board-page").then((module) => ({ default: module.RepositoryBoardPage })));

function LazyBoardRoute({ children }: { children: ReactNode }) {
  return <Suspense fallback={<div className="board-state state-loading" role="status"><span className="board-loader" aria-hidden="true" /><div>Opening change control…</div></div>}>{children}</Suspense>;
}

const contribution: FeatureContribution = {
  navigation: [{ label: "Changes", labelKey: "navigation.changes", to: "/changes", capability: "change_boards", icon: Workflow, order: 20 }],
  routes: [
    { path: "changes", element: <LazyBoardRoute><BoardWorkspacePage /></LazyBoardRoute> },
    { path: "changes/:orgId", element: <LazyBoardRoute><BoardListPage /></LazyBoardRoute> },
    { path: "changes/:orgId/repos/:repoId", element: <LazyBoardRoute><RepositoryBoardPage /></LazyBoardRoute> },
    { path: "changes/:orgId/repos/:repoId/:change", element: <LazyBoardRoute><BoardDetailPage /></LazyBoardRoute> },
    { path: ":owner/:repo/changes", element: <LazyBoardRoute><RepositoryBoardPage /></LazyBoardRoute> },
    { path: ":owner/:repo/changes/:change", element: <LazyBoardRoute><BoardDetailPage /></LazyBoardRoute> },
  ],
};

export default contribution;
