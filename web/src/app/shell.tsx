import { useState } from "react";
import { Link, NavLink, Navigate, Outlet, useLocation } from "react-router-dom";
import { AlertCircle, Boxes, KeyRound, LogIn, Menu, Settings2, X } from "lucide-react";
import { ErrorNotice, Loading } from "./components";
import { ProblemInspector } from "./problem-inspector";
import { useInspector } from "./problem-inspector";
import { useCurrentContext, useMeta } from "../auth/session";
import { isApiProblem } from "../lib/api/client";
import { featureNavigation } from "./feature-contributions";
import { Avatar } from "./avatar";
import { useTranslation } from "react-i18next";
import { LanguageSwitcher } from "../i18n/language-switcher";
import { isCanonicalRepositoryReadPath, isPublicUserProfilePath, isRepositoryRootOwner } from "../lib/canonical-routes";
import type { OrganizationContext } from "../lib/api/types";
import { ProfileOnboardingDialog } from "../auth/profile-onboarding-dialog";

export { isCanonicalRepositoryReadPath, isPublicUserProfilePath } from "../lib/canonical-routes";

const navClass = ({ isActive }: { isActive: boolean }) => isActive ? "nav-link active" : "nav-link";

function decodePathSegment(segment: string) {
  try { return decodeURIComponent(segment); } catch { return segment; }
}

export function resolveNavigationOrganization(pathname: string, organizations: OrganizationContext[]) {
  const segments = pathname.split("/").filter(Boolean).map(decodePathSegment);
  let candidate = "";
  if (segments[0] === "admin" && segments[1] === "orgs") candidate = segments[2] ?? "";
  else if (segments[0] === "orgs") candidate = segments[1] ?? "";
  else if (segments[0] === "_repos") candidate = segments[1] ?? "";
  else if (["issues", "changes", "search"].includes(segments[0]) && segments.length > 1) candidate = segments[1];
  else if (segments.length > 1 && isRepositoryRootOwner(segments[0])) candidate = segments[0];
  if (!candidate) return undefined;
  const normalized = candidate.toLowerCase();
  return organizations.find((organization) => organization.id === candidate || organization.name.toLowerCase() === normalized);
}

export function AuthenticatedShell() {
  const { t } = useTranslation();
  const contextQuery = useCurrentContext();
  const metaQuery = useMeta();
  const location = useLocation();
  const [mobileOpen, setMobileOpen] = useState(false);
  const inspector = useInspector();
  if (contextQuery.isLoading || metaQuery.isLoading) return <Loading />;
  if (contextQuery.error && isApiProblem(contextQuery.error) && contextQuery.error.problem.status === 401) {
    if (isCanonicalRepositoryReadPath(location.pathname) || isPublicUserProfilePath(location.pathname)) return <PublicRepositoryShell />;
    return <Navigate to="/login" replace state={{ returnTo: `${location.pathname}${location.search}${location.hash}` }} />;
  }
  if (contextQuery.error) {
    return <div className="public-narrow"><ErrorNotice error={contextQuery.error} /></div>;
  }
  if (!contextQuery.data) return <Navigate to="/login" replace />;
  const context = contextQuery.data;
  const features = metaQuery.data?.features;
  const currentOrganization = resolveNavigationOrganization(location.pathname, context.organizations);
  const visibleFeatureNav = featureNavigation.filter((item) => !item.capability || features?.[item.capability as keyof typeof features]);
  return <div className="app-shell">
    <a className="skip-link" href="#main-content">{t("navigation.skip")}</a>
    <header className="mobile-header">
      <Link className="brand compact" to="/"><span className="brand-mark">is</span><span>issue-spec</span></Link>
      <button className="icon-button inverse" type="button" onClick={() => setMobileOpen((open) => !open)} aria-expanded={mobileOpen} aria-controls="primary-navigation" aria-label={t("navigation.toggle")}>{mobileOpen ? <X /> : <Menu />}</button>
    </header>
    <nav id="primary-navigation" className={`sidebar ${mobileOpen ? "open" : ""}`} aria-label={t("navigation.primary")} onClick={(event) => { if ((event.target as Element).closest("a")) setMobileOpen(false); }}>
      <Link className="brand" to="/"><span className="brand-mark">is</span><span><strong>issue-spec</strong><small>{t("brand.controlRoom")}</small></span></Link>
      <div className="nav-context">
        <span className="eyebrow inverse-muted">{t("navigation.currentDesk")}</span>
        <strong>{currentOrganization?.display_name ?? t("navigation.allOrganizations")}</strong>
        <span>{currentOrganization ? t(`common.permission.${currentOrganization.effective_permission}`, { defaultValue: currentOrganization.effective_permission }) : t("navigation.chooseOrganization")}</span>
      </div>
      <div className="nav-group"><span className="nav-label">{t("navigation.workspace")}</span>
        {visibleFeatureNav.map((item) => <NavLink key={item.to} className={({ isActive }) => navClass({ isActive: isActive || Boolean(item.matches?.(location.pathname)) })} to={item.to}><item.icon size={18} aria-hidden="true" /><span>{item.labelKey ? t(item.labelKey) : item.label}</span></NavLink>)}
        <NavLink className={navClass} to="/" end><Boxes size={18} /><span>{t("navigation.repositories")}</span></NavLink>
      </div>
      <div className="nav-group"><span className="nav-label">{t("navigation.account")}</span>
        <NavLink className={navClass} to="/settings/account"><Avatar login={context.user.login} displayName={context.user.display_name} src={context.user.avatar_url} size={24} tone="inverse" /><span>{t("navigation.session")}</span></NavLink>
        <NavLink className={navClass} to="/settings/tokens"><KeyRound size={18} /><span>{t("navigation.tokens")}</span></NavLink>
        {context.allowed_actions.includes("site.admin") || context.organizations.some((org) => org.allowed_actions.includes("organization.admin")) ? <NavLink className={navClass} to="/admin"><Settings2 size={18} /><span>{t("navigation.administration")}</span></NavLink> : null}
      </div>
      <div className="nav-group nav-language"><LanguageSwitcher inverse /></div>
    </nav>
    <main id="main-content" className="main-content" tabIndex={-1}><Outlet /></main>
    <ProblemInspector identity={`@${context.user.login}`} permission={currentOrganization?.effective_permission} />
    <button id="inspector-toggle" className="inspector-toggle" type="button" onClick={inspector.openInspector} aria-expanded={inspector.open} aria-controls="request-inspector"><AlertCircle size={17} /><span>{inspector.state.problem ? t("navigation.requestProblem") : t("navigation.inspector")}</span></button>
    <nav className="bottom-nav" aria-label={t("navigation.mobile")} style={{ gridTemplateColumns: `repeat(${visibleFeatureNav.length + 2}, 1fr)` }}>
      {visibleFeatureNav.map((item) => <NavLink key={item.to} className={({ isActive }) => isActive || item.matches?.(location.pathname) ? "active" : undefined} to={item.to}><item.icon aria-hidden="true" /><span>{item.labelKey ? t(item.labelKey) : item.label}</span></NavLink>)}
      <NavLink to="/" end><Boxes /><span>{t("navigation.repositories")}</span></NavLink>
      <NavLink to="/settings/account"><Avatar login={context.user.login} displayName={context.user.display_name} src={context.user.avatar_url} size={24} /><span>{t("navigation.account")}</span></NavLink>
    </nav>
    <ProfileOnboardingDialog enabled={Boolean(features?.email_notifications)} />
  </div>;
}

export function PublicRepositoryShell() {
  const { t } = useTranslation();
  const location = useLocation();
  const returnTo = `${location.pathname}${location.search}${location.hash}`;
  return <div className="repository-public-shell">
    <a className="skip-link" href="#main-content">{t("navigation.skip")}</a>
    <header><Link className="brand public" to="/"><span className="brand-mark">is</span><span><strong>issue-spec</strong><small>{t("brand.publicView")}</small></span></Link><LanguageSwitcher /><Link className="button primary" to="/login" state={{ returnTo }}><LogIn size={16} />{t("navigation.signIn")}</Link></header>
    <main id="main-content" tabIndex={-1}><Outlet /></main>
  </div>;
}

export function PublicShell() {
  const { t } = useTranslation();
  return <div className="public-shell"><header><Link className="brand public" to="/"><span className="brand-mark">is</span><span><strong>issue-spec</strong><small>{t("brand.selfHostedDesk")}</small></span></Link><LanguageSwitcher /></header><main><Outlet /></main></div>;
}
