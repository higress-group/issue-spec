import { useState } from "react";
import { Link, NavLink, Navigate, Outlet, useLocation } from "react-router-dom";
import { AlertCircle, Boxes, KeyRound, LayoutDashboard, LogIn, Menu, Settings2, X } from "lucide-react";
import { ErrorNotice, Loading } from "./components";
import { ProblemInspector } from "./problem-inspector";
import { useInspector } from "./problem-inspector";
import { useCurrentContext, useMeta } from "../auth/session";
import { isApiProblem } from "../lib/api/client";
import { featureNavigation } from "./feature-contributions";
import { Avatar } from "./avatar";

const navClass = ({ isActive }: { isActive: boolean }) => isActive ? "nav-link active" : "nav-link";

export function AuthenticatedShell() {
  const contextQuery = useCurrentContext();
  const metaQuery = useMeta();
  const location = useLocation();
  const [mobileOpen, setMobileOpen] = useState(false);
  const inspector = useInspector();
  if (contextQuery.isLoading || metaQuery.isLoading) return <Loading />;
  if (contextQuery.error && isApiProblem(contextQuery.error) && contextQuery.error.problem.status === 401) {
    if (isCanonicalRepositoryReadPath(location.pathname)) return <PublicRepositoryShell />;
    return <Navigate to="/login" replace state={{ returnTo: `${location.pathname}${location.search}${location.hash}` }} />;
  }
  if (contextQuery.error) {
    return <div className="public-narrow"><ErrorNotice error={contextQuery.error} /></div>;
  }
  if (!contextQuery.data) return <Navigate to="/login" replace />;
  const context = contextQuery.data;
  const features = metaQuery.data?.features;
  const firstOrg = context.organizations[0];
  const visibleFeatureNav = featureNavigation.filter((item) => !item.capability || features?.[item.capability as keyof typeof features]);
  return <div className="app-shell">
    <a className="skip-link" href="#main-content">Skip to main content</a>
    <header className="mobile-header">
      <Link className="brand compact" to="/"><span className="brand-mark">is</span><span>issue-spec</span></Link>
      <button className="icon-button inverse" type="button" onClick={() => setMobileOpen((open) => !open)} aria-expanded={mobileOpen} aria-controls="primary-navigation" aria-label="Toggle navigation">{mobileOpen ? <X /> : <Menu />}</button>
    </header>
    <nav id="primary-navigation" className={`sidebar ${mobileOpen ? "open" : ""}`} aria-label="Primary navigation" onClick={(event) => { if ((event.target as Element).closest("a")) setMobileOpen(false); }}>
      <Link className="brand" to="/"><span className="brand-mark">is</span><span><strong>issue-spec</strong><small>workflow control room</small></span></Link>
      <div className="nav-context">
        <span className="eyebrow inverse-muted">Current desk</span>
        <strong>{firstOrg?.display_name ?? "No organization"}</strong>
        <span>{firstOrg?.effective_permission ?? "observer"}</span>
      </div>
      <div className="nav-group"><span className="nav-label">Workspace</span>
        <NavLink className={navClass} to="/" end><LayoutDashboard size={18} /><span>Overview</span></NavLink>
        {visibleFeatureNav.map((item) => <NavLink key={item.to} className={navClass} to={item.to}><item.icon size={18} aria-hidden="true" /><span>{item.label}</span></NavLink>)}
        {firstOrg ? <NavLink className={navClass} to={`/orgs/${firstOrg.id}/repos`}><Boxes size={18} /><span>Repositories</span></NavLink> : null}
      </div>
      <div className="nav-group"><span className="nav-label">Account</span>
        <NavLink className={navClass} to="/settings/account"><Avatar login={context.user.login} displayName={context.user.display_name} src={context.user.avatar_url} size={24} tone="inverse" /><span>Session</span></NavLink>
        <NavLink className={navClass} to="/settings/tokens"><KeyRound size={18} /><span>Access tokens</span></NavLink>
        {context.allowed_actions.includes("site.admin") || context.organizations.some((org) => org.allowed_actions.includes("organization.admin")) ? <NavLink className={navClass} to="/admin"><Settings2 size={18} /><span>Administration</span></NavLink> : null}
      </div>
    </nav>
    <main id="main-content" className="main-content" tabIndex={-1}><Outlet /></main>
    <ProblemInspector identity={`@${context.user.login}`} permission={firstOrg?.effective_permission} />
    <button id="inspector-toggle" className="inspector-toggle" type="button" onClick={inspector.openInspector} aria-expanded={inspector.open} aria-controls="request-inspector"><AlertCircle size={17} /><span>{inspector.state.problem ? "Request problem" : "Inspector"}</span></button>
    <nav className="bottom-nav" aria-label="Mobile navigation">
      <NavLink to="/" end><LayoutDashboard /><span>Home</span></NavLink>
      {visibleFeatureNav.map((item) => <NavLink key={item.to} to={item.to}><item.icon aria-hidden="true" /><span>{item.label}</span></NavLink>)}
      {firstOrg ? <NavLink to={`/orgs/${firstOrg.id}/repos`}><Boxes /><span>Repos</span></NavLink> : <span />}
      <NavLink to="/settings/account"><Avatar login={context.user.login} displayName={context.user.display_name} src={context.user.avatar_url} size={24} /><span>Account</span></NavLink>
    </nav>
  </div>;
}

export function isCanonicalRepositoryReadPath(pathname: string) {
  return /^\/[^/]+\/[^/]+\/(?:issues(?:\/[1-9]\d*)?|changes(?:\/[^/]+)?)\/?$/.test(pathname);
}

export function PublicRepositoryShell() {
  const location = useLocation();
  const returnTo = `${location.pathname}${location.search}${location.hash}`;
  return <div className="repository-public-shell">
    <a className="skip-link" href="#main-content">Skip to main content</a>
    <header><Link className="brand public" to="/"><span className="brand-mark">is</span><span><strong>issue-spec</strong><small>public repository view</small></span></Link><Link className="button primary" to="/login" state={{ returnTo }}><LogIn size={16} />Sign in</Link></header>
    <main id="main-content" tabIndex={-1}><Outlet /></main>
  </div>;
}

export function PublicShell() {
  return <div className="public-shell"><header><Link className="brand public" to="/"><span className="brand-mark">is</span><span><strong>issue-spec</strong><small>self-hosted workflow desk</small></span></Link></header><main><Outlet /></main></div>;
}
