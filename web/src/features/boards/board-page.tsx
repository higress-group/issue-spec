import { useQuery } from "@tanstack/react-query";
import type { ReactNode } from "react";
import { ArrowRight, Building2, Filter, RotateCcw, SearchX } from "lucide-react";
import { Link, useNavigate, useParams, useSearchParams } from "react-router-dom";
import { isApiProblem } from "../../lib/api/client";
import { boardApi } from "./api";
import { BoardState, BoardSummary, ChangeCard, DiagnosticSummary } from "./components";
import { anomalyCatalog, changeLifecycleSchema, changeStageSchema, type BoardFilters, type BoardPageModel } from "./types";
import { RepositoryGate, repositoryChangePath, type ActiveRepository } from "../issues/repository-context";

const perPage = 12;

export function BoardWorkspacePage() {
  const context = useQuery({ queryKey: ["boards", "context"], queryFn: ({ signal }) => boardApi.context(signal) });
  if (context.isLoading) return <BoardState>Opening change control…</BoardState>;
  if (!context.data) return <BoardState kind="error"><h1>Change control could not open</h1><p>Refresh the page or inspect the request details.</p></BoardState>;
  return <div className="board-page board-workspace">
    <header className="board-hero"><div><span className="board-eyebrow">Workflow control</span><h1>See the change, not the paperwork.</h1><p>Proposal, design, and implementation converge into one readable operational view.</p></div><div className="board-hero-orbit" aria-hidden="true"><span>01</span><span>02</span><span>03</span></div></header>
    <section className="organization-deck" aria-labelledby="organization-heading"><header><div><span className="board-eyebrow">Your organizations</span><h2 id="organization-heading">Choose a control surface</h2></div><small>{context.data.organizations.length} visible</small></header>
      <div className="organization-grid">{context.data.organizations.map((organization, index) => <Link className="organization-card" to={`/changes/${organization.id}`} key={organization.id}><span className="organization-index">{String(index + 1).padStart(2, "0")}</span><Building2 aria-hidden="true" /><span><strong>{organization.display_name}</strong><small>{organization.name} · {organization.effective_permission}</small></span><ArrowRight aria-hidden="true" /></Link>)}</div>
      {!context.data.organizations.length ? <BoardState kind="empty"><h2>No visible organizations</h2><p>Change boards appear after you receive organization read access.</p></BoardState> : null}
    </section>
  </div>;
}

function parseFilters(search: URLSearchParams): BoardFilters {
  const stage = changeStageSchema.safeParse(search.get("stage"));
  const lifecycle = changeLifecycleSchema.safeParse(search.get("lifecycle"));
  const page = Number(search.get("page") ?? "1");
  return {
    stage: stage.success ? stage.data : undefined,
    lifecycle: lifecycle.success ? lifecycle.data : undefined,
    anomaly: search.get("anomaly") || undefined,
    page: Number.isInteger(page) && page > 0 ? page : 1,
    perPage,
  };
}

function FilterBar({ filters, update, clear }: { filters: BoardFilters; update: (key: string, value: string) => void; clear: () => void }) {
  const active = Boolean(filters.stage || filters.lifecycle || filters.anomaly);
  return <section className="board-filters" aria-label="Change filters"><span className="filter-heading"><Filter aria-hidden="true" />Focus</span>
    <label><span>Stage</span><select value={filters.stage ?? ""} onChange={(event) => update("stage", event.target.value)}><option value="">All stages</option><option value="proposal">Proposal</option><option value="design">Design</option><option value="implement">Implement</option><option value="unknown">Unknown</option></select></label>
    <label><span>Lifecycle</span><select value={filters.lifecycle ?? ""} onChange={(event) => update("lifecycle", event.target.value)}><option value="">All states</option><option value="active">Active</option><option value="blocked">Blocked</option><option value="completed">Completed</option><option value="closed">Closed</option></select></label>
    <label><span>Diagnostic</span><select value={filters.anomaly ?? ""} onChange={(event) => update("anomaly", event.target.value)}><option value="">Any diagnostic</option>{Object.entries(anomalyCatalog).map(([code, copy]) => <option value={code} key={code}>{copy.label}</option>)}</select></label>
    <button type="button" className="filter-reset" onClick={clear} disabled={!active}><RotateCcw aria-hidden="true" />Clear all</button>
  </section>;
}

export function BoardListPage() {
  const { orgId = "" } = useParams();
  const navigate = useNavigate();
  const controls = useBoardControls();
  const context = useQuery({ queryKey: ["boards", "context"], queryFn: ({ signal }) => boardApi.context(signal) });
  const repositories = useQuery({ queryKey: ["boards", "repositories", orgId], queryFn: ({ signal }) => boardApi.repositories(orgId, signal), enabled: Boolean(orgId) });
  const organization = context.data?.organizations.find((item) => item.id === orgId);
  const board = useQuery({
    queryKey: ["boards", orgId, "organization", controls.filters],
    queryFn: ({ signal }) => boardApi.organizationBoard(orgId, controls.filters, signal),
    enabled: Boolean(orgId && organization),
  });
  if (context.isLoading || repositories.isLoading || board.isLoading) return <BoardState>Projecting visible changes…</BoardState>;
  if ((context.isSuccess && !organization) || isApiProblem(repositories.error, "not_found") || isApiProblem(repositories.error, "forbidden") ||
    isApiProblem(board.error, "not_found") || isApiProblem(board.error, "forbidden")) return <SafeBoardState />;
  if (context.error || repositories.error || board.error || !board.data || !organization) return <BoardState kind="error"><h1>Board unavailable</h1><p>The projection could not be loaded. Try again or inspect the request ID.</p></BoardState>;
  const repositoryOptions = repositories.data?.repositories ?? [];
  const scopeControl = <label className="context-switcher"><span>Board scope</span><select value="" onChange={(event) => {
    const selected = repositoryOptions.find(({ repository }) => repository.id === event.target.value);
    navigate(selected ? `/${encodeURIComponent(organization.name)}/${encodeURIComponent(selected.repository.name)}/changes` : `/changes/${orgId}`);
  }}><option value="">All visible repositories</option>{repositoryOptions.map(({ repository }) => <option key={repository.id} value={repository.id}>{repository.display_name}</option>)}</select></label>;
  return <BoardSurface owner={organization.name} title={`${organization.display_name} changes`} description="Every visible repository, aggregated only after permission filtering." board={board.data} controls={controls} scopeLabel="Organization board" scopeControl={scopeControl} />;
}

function useBoardControls() {
  const [search, setSearch] = useSearchParams();
  const filters = parseFilters(search);
  const update = (key: string, value: string) => { const next = new URLSearchParams(search); if (value) next.set(key, value); else next.delete(key); next.delete("page"); setSearch(next); };
  const clear = () => setSearch(new URLSearchParams());
  const goPage = (page: number) => { const next = new URLSearchParams(search); if (page > 1) next.set("page", String(page)); else next.delete("page"); setSearch(next); document.getElementById("change-results")?.focus(); };
  return { filters, update, clear, goPage };
}

function BoardSurface({ owner, title, description, board, controls, scopeLabel, scopeControl }: { owner: string; title: string; description: string; board: BoardPageModel; controls: ReturnType<typeof useBoardControls>; scopeLabel: string; scopeControl?: ReactNode }) {
  const hasNext = board.page * board.per_page < board.total;
  return <div className="board-page">
    <header className="board-masthead"><div><Link to="/changes" className="board-back">Change control</Link><span className="board-eyebrow">{scopeLabel}</span><h1>{title}</h1><p>{description}</p></div>{scopeControl}</header>
    <BoardSummary page={board} />
    <FilterBar filters={controls.filters} update={controls.update} clear={controls.clear} />
    <DiagnosticSummary diagnostics={board.diagnostics} />
    <section id="change-results" className="change-results" aria-labelledby="results-heading" tabIndex={-1}><header><div><span className="board-eyebrow">Current projection</span><h2 id="results-heading">{board.total} {board.total === 1 ? "change" : "changes"}</h2></div><span>Page {board.page}</span></header>
      {board.cards.length ? <div className="change-card-grid">{board.cards.map((card) => <ChangeCard key={`${card.repository.id}:${card.change_key}`} card={card} owner={owner} />)}</div> : <BoardState kind="empty"><SearchX aria-hidden="true" /><h2>No changes match this view</h2><p>Clear a filter or choose another repository.</p><button type="button" className="board-text-button" onClick={controls.clear}>Reset filters</button></BoardState>}
    </section>
    <nav className="board-pagination" aria-label="Change pages"><button type="button" disabled={board.page === 1} onClick={() => controls.goPage(board.page - 1)}>Previous</button><span><strong>{board.page}</strong><small>of {Math.max(1, Math.ceil(board.total / board.per_page))}</small></span><button type="button" disabled={!hasNext} onClick={() => controls.goPage(board.page + 1)}>Next</button></nav>
  </div>;
}

function RepositoryBoard({ active }: { active: ActiveRepository }) {
  const controls = useBoardControls();
  const board = useQuery({ queryKey: ["boards", active.organization.id, active.repository.repository.id, controls.filters], queryFn: ({ signal }) => boardApi.repositoryBoard(active.organization.id, active.repository.repository.id, controls.filters, signal) });
  if (board.isLoading) return <BoardState>Projecting visible changes…</BoardState>;
  if (isApiProblem(board.error, "not_found") || isApiProblem(board.error, "forbidden")) return <SafeBoardState />;
  if (board.error || !board.data) return <BoardState kind="error"><h1>Board unavailable</h1><p>The projection could not be loaded. Try again or inspect the request ID.</p></BoardState>;
  const scopeControl = active.authenticated ? <Link className="board-text-button" to={`/changes/${active.organization.id}`}>Organization board</Link> : <Link className="board-text-button" to="/login" state={{ returnTo: repositoryChangePath(active) }}>Sign in</Link>;
  return <BoardSurface owner={active.organization.name} title={active.repository.repository.display_name} description={`A focused workflow projection for ${active.repository.repository.name}.`} board={board.data} controls={controls} scopeLabel="Repository board" scopeControl={scopeControl} />;
}

export function RepositoryBoardPage() { return <RepositoryGate>{(active) => <RepositoryBoard active={active} />}</RepositoryGate>; }

export function SafeBoardState() {
  return <BoardState kind="safe"><h1>This change board is not available</h1><p>It may not exist, or your current credential cannot see it.</p><Link className="board-state-link" to="/changes">Return to visible organizations</Link></BoardState>;
}
