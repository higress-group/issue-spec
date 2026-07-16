import { useQuery } from "@tanstack/react-query";
import { ArrowRight, Building2, ChevronLeft, ChevronRight, FileSearch, MessageSquareText, RotateCcw, Search, Workflow } from "lucide-react";
import { useRef, type FormEvent, type ReactNode } from "react";
import { Link, useNavigate, useParams, useSearchParams } from "react-router-dom";
import { useCurrentContext, useMeta } from "../../auth/session";
import { isApiProblem } from "../../lib/api/client";
import type { OrganizationContext, RepositoryContext } from "../../lib/api/types";
import { searchApi } from "./api";
import { searchSourceSchema, searchStageSchema, searchStateSchema, type SearchFilters, type SearchIssueModel, type SearchPageModel } from "./types";

const perPage = 12;

function CapabilityGate({ children }: { children: ReactNode }) {
  const meta = useMeta();
  if (meta.isLoading) return <SearchState>Discovering search capability…</SearchState>;
  if (meta.error) return <SearchState kind="error"><h1>Search capability is unavailable</h1><p>Refresh the page or inspect the request details.</p></SearchState>;
  if (!meta.data?.features.search) return <SearchState kind="empty"><FileSearch aria-hidden="true" /><h1>Search is not enabled</h1><p>Ask the server operator to configure PostgreSQL search mode.</p></SearchState>;
  return children;
}

export function SearchWorkspacePage() {
  return <CapabilityGate><SearchWorkspace /></CapabilityGate>;
}

function SearchWorkspace() {
  const context = useCurrentContext();
  if (context.isLoading) return <SearchState>Opening visible organizations…</SearchState>;
  if (context.error || !context.data) return <SearchState kind="error"><h1>Search could not open</h1><p>Your visible organization context is unavailable.</p></SearchState>;
  return <div className="search-page search-workspace">
    <header className="search-hero"><div><span className="search-eyebrow">Discussion memory</span><h1>Find the decision before changing the code.</h1><p>Search issue bodies, comments, and prior change keys without leaving the self-hosted workspace.</p></div><Search aria-hidden="true" /></header>
    <section className="search-scope-deck" aria-labelledby="search-scope-heading"><header><div><span className="search-eyebrow">Search scope</span><h2 id="search-scope-heading">Choose an organization</h2></div><small>{context.data.organizations.length} visible</small></header>
      <div className="search-scope-grid">{context.data.organizations.map((organization) => <Link key={organization.id} to={`/search/${organization.id}`} className="search-scope-card"><Building2 aria-hidden="true" /><span><strong>{organization.display_name}</strong><small>{organization.name} · {organization.effective_permission}</small></span><ArrowRight aria-hidden="true" /></Link>)}</div>
      {!context.data.organizations.length ? <SearchState kind="empty"><h2>No searchable organizations</h2><p>Search appears after you receive repository read access.</p></SearchState> : null}
    </section>
  </div>;
}

export function OrganizationSearchPage() {
  const { orgId = "" } = useParams();
  const context = useCurrentContext();
  const repositories = useQuery({ queryKey: ["search", "repositories", orgId], queryFn: ({ signal }) => searchApi.repositories(orgId, signal), enabled: Boolean(orgId) });
  if (context.isLoading || repositories.isLoading) return <SearchState>Opening search scope…</SearchState>;
  const organization = context.data?.organizations.find((item) => item.id === orgId);
  if (!organization || isApiProblem(repositories.error, "not_found") || isApiProblem(repositories.error, "forbidden")) return <SafeSearchState />;
  if (context.error || repositories.error) return <SearchState kind="error"><h1>Search scope is unavailable</h1><p>Try again or inspect the request details.</p></SearchState>;
  return <CapabilityGate><SearchSurface organization={organization} repositories={repositories.data?.repositories ?? []} /></CapabilityGate>;
}

export function RepositorySearchPage() {
  const { orgId = "", repoId = "" } = useParams();
  const context = useCurrentContext();
  const repositories = useQuery({ queryKey: ["search", "repositories", orgId], queryFn: ({ signal }) => searchApi.repositories(orgId, signal), enabled: Boolean(orgId) });
  if (context.isLoading || repositories.isLoading) return <SearchState>Opening repository search…</SearchState>;
  const organization = context.data?.organizations.find((item) => item.id === orgId);
  const repository = repositories.data?.repositories.find((item) => item.repository.id === repoId);
  if (!organization || !repository || isApiProblem(repositories.error, "not_found") || isApiProblem(repositories.error, "forbidden")) return <SafeSearchState />;
  if (context.error || repositories.error) return <SearchState kind="error"><h1>Search scope is unavailable</h1><p>Try again or inspect the request details.</p></SearchState>;
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
    <header className="search-masthead"><div><Link to="/search" className="search-back">Search workspace</Link><span className="search-eyebrow">{repository ? "Repository memory" : "Organization memory"}</span><h1>{title}</h1><p>Issue and comment text stays grouped by its original discussion, with related change metadata alongside it.</p></div>
      <label className="search-scope-select"><span>Search scope</span><select value={repository?.repository.id ?? ""} onChange={(event) => switchScope(event.target.value)}><option value="">All visible repositories</option>{repositories.map((item) => <option key={item.repository.id} value={item.repository.id}>{item.repository.display_name}</option>)}</select></label></header>
    <form className="search-controls" role="search" onSubmit={submit}><label className="search-query"><span>Search issue discussions</span><div><Search aria-hidden="true" /><input key={filters.query} ref={queryRef} defaultValue={filters.query} maxLength={256} placeholder="change key, error, decision, or code symbol" /><button type="submit">Search</button></div></label>
      <div className="search-filters"><label><span>State</span><select value={filters.state} onChange={(event) => update("state", event.target.value)}><option value="all">Open and closed</option><option value="open">Open</option><option value="closed">Closed</option></select></label>
        <label><span>Match</span><select value={filters.source} onChange={(event) => update("source", event.target.value)}><option value="all">Issue or comment</option><option value="issue">Issue text</option><option value="comments">Comments</option><option value="change">Change key</option></select></label>
        <label><span>Change stage</span><select value={filters.stage ?? ""} onChange={(event) => update("stage", event.target.value)}><option value="">Any stage</option><option value="proposal">Proposal</option><option value="design">Design</option><option value="implement">Implement</option></select></label>
        <button className="search-reset" type="button" onClick={clear} disabled={!filters.query && filters.state === "all" && filters.source === "all" && !filters.stage}><RotateCcw aria-hidden="true" />Reset</button></div></form>
    <SearchResults query={filters.query} page={results.data} loading={results.isLoading} error={results.error} goPage={goPage} />
  </div>;
}

function SearchResults({ query, page, loading, error, goPage }: { query: string; page?: SearchPageModel; loading: boolean; error: unknown; goPage: (page: number) => void }) {
  if (!query) return <SearchState kind="empty"><FileSearch aria-hidden="true" /><h2>Search earlier discussions</h2><p>Enter a decision, failure mode, change key, or code symbol.</p></SearchState>;
  if (loading) return <SearchState>Searching visible discussions…</SearchState>;
  if (isApiProblem(error, "not_found") || isApiProblem(error, "forbidden")) return <SafeSearchState />;
  if (error || !page) return <SearchState kind="error"><h2>Search failed</h2><p>Try again or inspect the request details.</p></SearchState>;
  return <section id="search-results" className="search-results" tabIndex={-1} aria-labelledby="search-results-heading"><header><div><span className="search-eyebrow">Discussion matches</span><h2 id="search-results-heading">{page.items.length ? `${page.items.length} results on this page` : "No matching discussions"}</h2></div><span>Page {page.page}</span></header>
    {page.items.length ? <div className="search-result-list">{page.items.map((item) => <SearchResultCard key={item.id} item={item} />)}</div> : <SearchState kind="empty"><FileSearch aria-hidden="true" /><h2>Nothing matched this scope</h2><p>Try a shorter term, include closed issues, or search comments.</p></SearchState>}
    <nav className="search-pagination" aria-label="Search result pages"><button type="button" disabled={page.page === 1} onClick={() => goPage(page.page - 1)}><ChevronLeft aria-hidden="true" />Previous</button><span>Page <strong>{page.page}</strong></span><button type="button" disabled={!page.has_next} onClick={() => goPage(page.page + 1)}>Next<ChevronRight aria-hidden="true" /></button></nav>
  </section>;
}

function SearchResultCard({ item }: { item: SearchIssueModel }) {
  const issuePath = `/issues/${item.organization_id}/${item.repository_id}/${item.number}`;
  return <article className="search-result-card"><header><div><span className={`search-state-pill ${item.state}`}>{item.state}</span><span>{item.organization} / {item.repository} · #{item.number}</span></div><time dateTime={item.updated_at}>{new Intl.DateTimeFormat(undefined, { dateStyle: "medium" }).format(new Date(item.updated_at))}</time></header>
    <h3><Link to={issuePath}>{item.title}</Link></h3>
    {item.changes.length ? <div className="search-changes">{item.changes.map((change) => <Link key={change.key} to={`/changes/${item.organization_id}/repos/${item.repository_id}/${encodeURIComponent(change.key)}`}><Workflow aria-hidden="true" />{change.key}<span>{change.stage}</span></Link>)}</div> : null}
    <div className="search-matches">{item.matches.map((match, index) => <div className="search-match" key={`${match.source}:${match.comment_id ?? index}`}><span>{match.source === "comment" ? <MessageSquareText aria-hidden="true" /> : <FileSearch aria-hidden="true" />}{match.source}</span><p>{match.excerpt}</p></div>)}</div>
    <Link className="search-open" to={issuePath}>Open full discussion<ArrowRight aria-hidden="true" /></Link>
  </article>;
}

function SearchState({ children, kind = "loading" }: { children: ReactNode; kind?: "loading" | "empty" | "error" | "safe" }) {
  return <div className={`search-state ${kind}`} role={kind === "loading" ? "status" : undefined}>{kind === "loading" ? <span className="search-loader" aria-hidden="true" /> : null}{children}</div>;
}

function SafeSearchState() { return <SearchState kind="safe"><h1>This search scope is not available</h1><p>It may not exist, or your current credential cannot see it.</p><Link to="/search">Return to visible organizations</Link></SearchState>; }
