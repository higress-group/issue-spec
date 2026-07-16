import { lazy, Suspense, type ReactNode } from "react";
import { Search } from "lucide-react";
import type { FeatureContribution } from "../../app/feature-contributions";
import "./search.css";

const SearchWorkspacePage = lazy(() => import("./search-page").then((module) => ({ default: module.SearchWorkspacePage })));
const OrganizationSearchPage = lazy(() => import("./search-page").then((module) => ({ default: module.OrganizationSearchPage })));
const RepositorySearchPage = lazy(() => import("./search-page").then((module) => ({ default: module.RepositorySearchPage })));

function LazySearchRoute({ children }: { children: ReactNode }) {
  return <Suspense fallback={<div className="search-state" role="status"><span className="search-loader" aria-hidden="true" />Opening search…</div>}>{children}</Suspense>;
}

const contribution: FeatureContribution = {
  navigation: [{ label: "Search", labelKey: "navigation.search", to: "/search", capability: "search", icon: Search, order: 15 }],
  routes: [
    { path: "search", element: <LazySearchRoute><SearchWorkspacePage /></LazySearchRoute> },
    { path: "search/:orgId", element: <LazySearchRoute><OrganizationSearchPage /></LazySearchRoute> },
    { path: "search/:orgId/repos/:repoId", element: <LazySearchRoute><RepositorySearchPage /></LazySearchRoute> },
  ],
};

export default contribution;
