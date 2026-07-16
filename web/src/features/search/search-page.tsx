import { useQuery } from "@tanstack/react-query";
import { ArrowRight, Building2, ChevronLeft, ChevronRight, FileSearch, MessageSquareText, RotateCcw, Search, Workflow } from "lucide-react";
import { useRef, type FormEvent, type ReactNode } from "react";
import { useTranslation } from "react-i18next";
import { Link, useNavigate, useParams, useSearchParams } from "react-router-dom";
import { useCurrentContext, useMeta } from "../../auth/session";
import { isApiProblem } from "../../lib/api/client";
import type { OrganizationContext, RepositoryContext } from "../../lib/api/types";
import { searchApi } from "./api";
import { searchSourceSchema, searchStageSchema, searchStateSchema, type SearchFilters, type SearchIssueModel, type SearchPageModel } from "./types";

const perPage = 12;

function CapabilityGate({ children }: { children: ReactNode }) {
  const { t } = useTranslation();
  const meta = useMeta();
  if (meta.isLoading) return <SearchState>{t("search.capability.discovering")}</SearchState>;
  if (meta.error) return <SearchState kind="error"><h1>{t("search.capability.unavailableTitle")}</h1><p>{t("search.capability.unavailableDescription")}</p></SearchState>;
  if (!meta.data?.features.search) return <SearchState kind="empty"><FileSearch aria-hidden="true" /><h1>{t("search.capability.disabledTitle")}</h1><p>{t("search.capability.disabledDescription")}</p></SearchState>;
  return children;
}

export function SearchWorkspacePage() {
  return <CapabilityGate><SearchWorkspace /></CapabilityGate>;
}

function SearchWorkspace() {
  const { t } = useTranslation();
  const context = useCurrentContext();
  if (context.isLoading) return <SearchState>{t("search.workspace.openingOrganizations")}</SearchState>;
  if (context.error || !context.data) return <SearchState kind="error"><h1>{t("search.workspace.unavailableTitle")}</h1><p>{t("search.workspace.unavailableDescription")}</p></SearchState>;
  return <div className="search-page search-workspace">
    <header className="search-hero"><div><span className="search-eyebrow">{t("search.workspace.eyebrow")}</span><h1>{t("search.workspace.title")}</h1><p>{t("search.workspace.description")}</p></div><Search aria-hidden="true" /></header>
    <section className="search-scope-deck" aria-labelledby="search-scope-heading"><header><div><span className="search-eyebrow">{t("search.workspace.scopeEyebrow")}</span><h2 id="search-scope-heading">{t("search.workspace.chooseOrganization")}</h2></div><small>{t("search.workspace.visible", { count: context.data.organizations.length })}</small></header>
      <div className="search-scope-grid">{context.data.organizations.map((organization) => <Link key={organization.id} to={`/search/${organization.id}`} className="search-scope-card"><Building2 aria-hidden="true" /><span><strong>{organization.display_name}</strong><small>{organization.name} · {t(`common.permission.${organization.effective_permission}`, { defaultValue: organization.effective_permission })}</small></span><ArrowRight aria-hidden="true" /></Link>)}</div>
      {!context.data.organizations.length ? <SearchState kind="empty"><h2>{t("search.workspace.noOrganizationsTitle")}</h2><p>{t("search.workspace.noOrganizationsDescription")}</p></SearchState> : null}
    </section>
  </div>;
}

export function OrganizationSearchPage() {
  const { t } = useTranslation();
  const { orgId = "" } = useParams();
  const context = useCurrentContext();
  const repositories = useQuery({ queryKey: ["search", "repositories", orgId], queryFn: ({ signal }) => searchApi.repositories(orgId, signal), enabled: Boolean(orgId) });
  if (context.isLoading || repositories.isLoading) return <SearchState>{t("search.scope.opening")}</SearchState>;
  const organization = context.data?.organizations.find((item) => item.id === orgId);
  if (!organization || isApiProblem(repositories.error, "not_found") || isApiProblem(repositories.error, "forbidden")) return <SafeSearchState />;
  if (context.error || repositories.error) return <SearchState kind="error"><h1>{t("search.scope.unavailableTitle")}</h1><p>{t("search.scope.unavailableDescription")}</p></SearchState>;
  return <CapabilityGate><SearchSurface organization={organization} repositories={repositories.data?.repositories ?? []} /></CapabilityGate>;
}

export function RepositorySearchPage() {
  const { t } = useTranslation();
  const { orgId = "", repoId = "" } = useParams();
  const context = useCurrentContext();
  const repositories = useQuery({ queryKey: ["search", "repositories", orgId], queryFn: ({ signal }) => searchApi.repositories(orgId, signal), enabled: Boolean(orgId) });
  if (context.isLoading || repositories.isLoading) return <SearchState>{t("search.scope.openingRepository")}</SearchState>;
  const organization = context.data?.organizations.find((item) => item.id === orgId);
  const repository = repositories.data?.repositories.find((item) => item.repository.id === repoId);
  if (!organization || !repository || isApiProblem(repositories.error, "not_found") || isApiProblem(repositories.error, "forbidden")) return <SafeSearchState />;
  if (context.error || repositories.error) return <SearchState kind="error"><h1>{t("search.scope.unavailableTitle")}</h1><p>{t("search.scope.unavailableDescription")}</p></SearchState>;
  return <CapabilityGate><SearchSurface organization={organization} repositories={repositories.data?.repositories ?? []} repository={repository} /></CapabilityGate>;
}

function parseFilters(search: URLSearchParams): SearchFilters {
  const state = searchStateSchema.safeParse(search.get("state"));
  const source = searchSourceSchema.safeParse(search.get("source"));
  const stage = searchStageSchema.safeParse(search.get("stage"));
  const page = Number(search.get("page") ?? "1");
  return { query: (search.get("q") ?? "").trim(), state: state.success ? state.data : "all",
    source: source.success ? source.data : "all", stage: stage.success ? stage.data : undefined,
    page: Number.isInteger(page) && page > 0 ? page : 1, perPage };
}

export function SearchSurface({ organization, repositories, repository }: { organization: OrganizationContext; repositories: RepositoryContext[]; repository?: RepositoryContext }) {
  const { t } = useTranslation();
  const navigate = useNavigate();
  const [search, setSearch] = useSearchParams();
  const filters = parseFilters(search);
  const queryRef = useRef<HTMLInputElement>(null);
  const results = useQuery({ queryKey: ["search", organization.id, repository?.repository.id ?? "organization", filters],
    queryFn: ({ signal }) => repository ? searchApi.repository(organization.id, repository.repository.id, filters, signal) : searchApi.organization(organization.id, filters, signal),
    enabled: filters.query.length > 0 });
  const update = (key: string, value: string) => { const next = new URLSearchParams(search); if (value && value !== "all") next.set(key, value); else next.delete(key); next.delete("page"); setSearch(next); };
  const submit = (event: FormEvent) => { event.preventDefault(); const next = new URLSearchParams(search); const query = queryRef.current?.value.trim() ?? ""; if (query) next.set("q", query); else next.delete("q"); next.delete("page"); setSearch(next); };
  const clear = () => { if (queryRef.current) queryRef.current.value = ""; setSearch(new URLSearchParams()); };
  const goPage = (page: number) => { const next = new URLSearchParams(search); if (page > 1) next.set("page", String(page)); else next.delete("page"); setSearch(next); document.getElementById("search-results")?.focus(); };
  const switchScope = (value: string) => { const suffix = search.toString() ? `?${search}` : ""; navigate(value ? `/search/${organization.id}/repos/${value}${suffix}` : `/search/${organization.id}${suffix}`); };
  const title = repository?.repository.display_name ?? organization.display_name;
  return <div className="search-page">
    <header className="search-masthead"><div><Link to="/search" className="search-back">{t("search.scope.workspace")}</Link><span className="search-eyebrow">{t(repository ? "search.scope.repositoryMemory" : "search.scope.organizationMemory")}</span><h1>{title}</h1><p>{t("search.scope.description")}</p></div>
      <label className="search-scope-select"><span>{t("search.scope.label")}</span><select value={repository?.repository.id ?? ""} onChange={(event) => switchScope(event.target.value)}><option value="">{t("search.scope.allRepositories")}</option>{repositories.map((item) => <option key={item.repository.id} value={item.repository.id}>{item.repository.display_name}</option>)}</select></label></header>
    <form className="search-controls" role="search" onSubmit={submit}><label className="search-query"><span>{t("search.controls.queryLabel")}</span><div><Search aria-hidden="true" /><input key={filters.query} ref={queryRef} defaultValue={filters.query} maxLength={256} placeholder={t("search.controls.placeholder")} /><button type="submit">{t("search.controls.submit")}</button></div></label>
      <div className="search-filters"><label><span>{t("search.controls.state")}</span><select value={filters.state} onChange={(event) => update("state", event.target.value)}><option value="all">{t("search.controls.stateAll")}</option><option value="open">{t("search.controls.stateOpen")}</option><option value="closed">{t("search.controls.stateClosed")}</option></select></label>
        <label><span>{t("search.controls.match")}</span><select value={filters.source} onChange={(event) => update("source", event.target.value)}><option value="all">{t("search.controls.matchAll")}</option><option value="issue">{t("search.controls.matchIssue")}</option><option value="comments">{t("search.controls.matchComments")}</option><option value="change">{t("search.controls.matchChange")}</option></select></label>
        <label><span>{t("search.controls.stage")}</span><select value={filters.stage ?? ""} onChange={(event) => update("stage", event.target.value)}><option value="">{t("search.controls.stageAny")}</option><option value="proposal">{t("search.controls.stageProposal")}</option><option value="design">{t("search.controls.stageDesign")}</option><option value="implement">{t("search.controls.stageImplement")}</option></select></label>
        <button className="search-reset" type="button" onClick={clear} disabled={!filters.query && filters.state === "all" && filters.source === "all" && !filters.stage}><RotateCcw aria-hidden="true" />{t("search.controls.reset")}</button></div></form>
    <SearchResults query={filters.query} page={results.data} loading={results.isLoading} error={results.error} goPage={goPage} />
  </div>;
}

function SearchResults({ query, page, loading, error, goPage }: { query: string; page?: SearchPageModel; loading: boolean; error: unknown; goPage: (page: number) => void }) {
  const { t } = useTranslation();
  if (!query) return <SearchState kind="empty"><FileSearch aria-hidden="true" /><h2>{t("search.results.initialTitle")}</h2><p>{t("search.results.initialDescription")}</p></SearchState>;
  if (loading) return <SearchState>{t("search.results.searching")}</SearchState>;
  if (isApiProblem(error, "not_found") || isApiProblem(error, "forbidden")) return <SafeSearchState />;
  if (error || !page) return <SearchState kind="error"><h2>{t("search.results.failedTitle")}</h2><p>{t("search.results.failedDescription")}</p></SearchState>;
  const groups = groupSearchResults(page.items);
  return <section id="search-results" className="search-results" tabIndex={-1} aria-labelledby="search-results-heading"><header><div><span className="search-eyebrow">{t("search.results.eyebrow")}</span><h2 id="search-results-heading">{page.items.length ? t("search.results.matching", { count: page.total }) : t("search.results.noMatches")}</h2></div><span>{t("search.results.shown", { page: page.page, count: page.items.length })}</span></header>
    {page.items.length ? <div className="search-result-list">{groups.map((group) => group.change
      ? <ChangeResultGroup key={group.id} group={group} />
      : <SearchResultCard key={group.id} item={group.items[0]} />)}</div> : <SearchState kind="empty"><FileSearch aria-hidden="true" /><h2>{t("search.results.emptyTitle")}</h2><p>{t("search.results.emptyDescription")}</p></SearchState>}
    <nav className="search-pagination" aria-label={t("search.results.pages")}><button type="button" disabled={page.page === 1} onClick={() => goPage(page.page - 1)}><ChevronLeft aria-hidden="true" />{t("search.results.previous")}</button><span>{t("search.results.page", { page: page.page })}</span><button type="button" disabled={!page.has_next} onClick={() => goPage(page.page + 1)}>{t("search.results.next")}<ChevronRight aria-hidden="true" /></button></nav>
  </section>;
}

type SearchResultGroup = {
  id: string;
  change?: SearchIssueModel["changes"][number];
  items: SearchIssueModel[];
};

export function groupSearchResults(items: SearchIssueModel[]): SearchResultGroup[] {
  const groups: SearchResultGroup[] = [];
  const changes = new Map<string, SearchResultGroup>();
  for (const item of items) {
    const matchedChanges = item.changes.filter((change) => change.matched);
    const change = matchedChanges.length === 1 ? matchedChanges[0] : matchedChanges.length === 0 && item.changes.length === 1 ? item.changes[0] : undefined;
    if (!change) {
      groups.push({ id: `issue:${item.id}`, items: [item] });
      continue;
    }
    const id = `change:${item.repository_id}:${change.key}`;
    let group = changes.get(id);
    if (!group) {
      group = { id, change, items: [] };
      changes.set(id, group);
      groups.push(group);
    } else if (stageRank(change.stage) > stageRank(group.change?.stage)) {
      group.change = change;
    }
    group.items.push(item);
  }
  return groups;
}

function stageRank(stage?: string) {
  return { unknown: 0, proposal: 1, design: 2, implement: 3 }[stage ?? "unknown"] ?? 0;
}

function ChangeResultGroup({ group }: { group: SearchResultGroup }) {
  const { t } = useTranslation();
  if (!group.change) return null;
  const first = group.items[0];
  const headingID = `search-change-${first.repository_id}-${group.change.key.replace(/[^a-zA-Z0-9_-]/g, "-")}`;
  const changePath = `/changes/${first.organization_id}/repos/${first.repository_id}/${encodeURIComponent(group.change.key)}`;
  return <section className="search-change-group" aria-labelledby={headingID}>
    <header><div><span className="search-eyebrow">{t("search.results.relatedChange")}</span><h3 id={headingID}><Link to={changePath}><Workflow aria-hidden="true" />{t("search.results.changeHeading", { key: group.change.key })}</Link></h3></div><span>{t("search.results.artifacts", { count: group.items.length, stage: t(`search.value.stage.${group.change.stage}`) })}</span></header>
    <div className="search-change-artifacts">{group.items.map((item) => <SearchResultCard key={item.id} item={item} />)}</div>
  </section>;
}

function SearchResultCard({ item }: { item: SearchIssueModel }) {
  const { t, i18n } = useTranslation();
  const issuePath = `/issues/${item.organization_id}/${item.repository_id}/${item.number}`;
  return <article className="search-result-card"><header><div><span className={`search-state-pill ${item.state}`}>{t(`search.value.state.${item.state}`)}</span><span>{item.organization} / {item.repository} · #{item.number}</span></div><time dateTime={item.updated_at}>{new Intl.DateTimeFormat(i18n.resolvedLanguage, { dateStyle: "medium" }).format(new Date(item.updated_at))}</time></header>
    <h3><Link to={issuePath}>{item.title}</Link></h3>
    {item.changes.length ? <div className="search-changes">{item.changes.map((change) => <Link key={change.key} to={`/changes/${item.organization_id}/repos/${item.repository_id}/${encodeURIComponent(change.key)}`}><Workflow aria-hidden="true" />{change.key}<span>{t(`search.value.stage.${change.stage}`)}</span></Link>)}</div> : null}
    <div className="search-matches">{item.matches.map((match, index) => <div className="search-match" key={`${match.source}:${match.comment_id ?? index}`}><span>{match.source === "comment" ? <MessageSquareText aria-hidden="true" /> : <FileSearch aria-hidden="true" />}{t(`search.value.source.${match.source}`)}</span><p>{match.excerpt}</p></div>)}</div>
    <Link className="search-open" to={issuePath}>{t("search.results.openDiscussion")}<ArrowRight aria-hidden="true" /></Link>
  </article>;
}

function SearchState({ children, kind = "loading" }: { children: ReactNode; kind?: "loading" | "empty" | "error" | "safe" }) {
  return <div className={`search-state ${kind}`} role={kind === "loading" ? "status" : undefined}>{kind === "loading" ? <span className="search-loader" aria-hidden="true" /> : null}{children}</div>;
}

function SafeSearchState() {
  const { t } = useTranslation();
  return <SearchState kind="safe"><h1>{t("search.safe.title")}</h1><p>{t("search.safe.description")}</p><Link to="/search">{t("search.safe.return")}</Link></SearchState>;
}
