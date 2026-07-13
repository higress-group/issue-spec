import { useQuery } from "@tanstack/react-query";
import type { ReactNode } from "react";
import { ArrowRight, Building2, Filter, RotateCcw, SearchX } from "lucide-react";
import { Link, useNavigate, useParams, useSearchParams } from "react-router-dom";
import { useTranslation } from "react-i18next";
import { isApiProblem } from "../../lib/api/client";
import { boardApi } from "./api";
import { BoardState, BoardSummary, ChangeCard, DiagnosticSummary } from "./components";
import { anomalyCatalog, changeLifecycleSchema, changeStageSchema, type BoardFilters, type BoardPageModel } from "./types";
import { RepositoryGate, repositoryChangePath, type ActiveRepository } from "../issues/repository-context";

const perPage = 12;

export function BoardWorkspacePage() {
  const { t } = useTranslation();
  const context = useQuery({ queryKey: ["boards", "context"], queryFn: ({ signal }) => boardApi.context(signal) });
  if (context.isLoading) return <BoardState>{t("changes.workspace.opening")}</BoardState>;
  if (!context.data) return <BoardState kind="error"><h1>{t("changes.workspace.unavailableTitle")}</h1><p>{t("changes.workspace.unavailableDescription")}</p></BoardState>;
  return <div className="board-page board-workspace">
    <header className="board-hero"><div><span className="board-eyebrow">{t("changes.workspace.eyebrow")}</span><h1>{t("changes.workspace.title")}</h1><p>{t("changes.workspace.description")}</p></div><div className="board-hero-orbit" aria-hidden="true"><span>01</span><span>02</span><span>03</span></div></header>
    <section className="organization-deck" aria-labelledby="organization-heading"><header><div><span className="board-eyebrow">{t("changes.workspace.organizations")}</span><h2 id="organization-heading">{t("changes.workspace.choose")}</h2></div><small>{t("changes.workspace.visible", { count: context.data.organizations.length })}</small></header>
      <div className="organization-grid">{context.data.organizations.map((organization, index) => <Link className="organization-card" to={`/changes/${organization.id}`} key={organization.id}><span className="organization-index">{String(index + 1).padStart(2, "0")}</span><Building2 aria-hidden="true" /><span><strong>{organization.display_name}</strong><small>{organization.name} · {t(`common.permission.${organization.effective_permission}`)}</small></span><ArrowRight aria-hidden="true" /></Link>)}</div>
      {!context.data.organizations.length ? <BoardState kind="empty"><h2>{t("changes.workspace.emptyTitle")}</h2><p>{t("changes.workspace.emptyDescription")}</p></BoardState> : null}
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
  const { t } = useTranslation();
  const active = Boolean(filters.stage || filters.lifecycle || filters.anomaly);
  return <section className="board-filters" aria-label={t("changes.filters.aria")}><span className="filter-heading"><Filter aria-hidden="true" />{t("changes.filters.focus")}</span>
    <label><span>{t("changes.filters.stage")}</span><select value={filters.stage ?? ""} onChange={(event) => update("stage", event.target.value)}><option value="">{t("changes.filters.allStages")}</option><option value="proposal">{t("changes.stage.proposal")}</option><option value="design">{t("changes.stage.design")}</option><option value="implement">{t("changes.stage.implement")}</option><option value="unknown">{t("changes.stage.unknown")}</option></select></label>
    <label><span>{t("changes.filters.lifecycle")}</span><select value={filters.lifecycle ?? ""} onChange={(event) => update("lifecycle", event.target.value)}><option value="">{t("changes.filters.allStates")}</option><option value="active">{t("changes.lifecycle.active")}</option><option value="blocked">{t("changes.lifecycle.blocked")}</option><option value="completed">{t("changes.lifecycle.completed")}</option><option value="closed">{t("changes.lifecycle.closed")}</option></select></label>
    <label><span>{t("changes.filters.diagnostic")}</span><select value={filters.anomaly ?? ""} onChange={(event) => update("anomaly", event.target.value)}><option value="">{t("changes.filters.anyDiagnostic")}</option>{Object.entries(anomalyCatalog).map(([code, copy]) => <option value={code} key={code}>{t(`changes.anomalies.${code}.label`, { defaultValue: copy.label })}</option>)}</select></label>
    <button type="button" className="filter-reset" onClick={clear} disabled={!active}><RotateCcw aria-hidden="true" />{t("changes.filters.clear")}</button>
  </section>;
}

export function BoardListPage() {
  const { t } = useTranslation();
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
  if (context.isLoading || repositories.isLoading || board.isLoading) return <BoardState>{t("changes.board.projecting")}</BoardState>;
  if ((context.isSuccess && !organization) || isApiProblem(repositories.error, "not_found") || isApiProblem(repositories.error, "forbidden") ||
    isApiProblem(board.error, "not_found") || isApiProblem(board.error, "forbidden")) return <SafeBoardState />;
  if (context.error || repositories.error || board.error || !board.data || !organization) return <BoardState kind="error"><h1>{t("changes.board.unavailableTitle")}</h1><p>{t("changes.board.unavailableDescription")}</p></BoardState>;
  const repositoryOptions = repositories.data?.repositories ?? [];
  const scopeControl = <label className="context-switcher"><span>{t("changes.board.scope")}</span><select value="" onChange={(event) => {
    const selected = repositoryOptions.find(({ repository }) => repository.id === event.target.value);
    navigate(selected ? `/${encodeURIComponent(organization.name)}/${encodeURIComponent(selected.repository.name)}/changes` : `/changes/${orgId}`);
  }}><option value="">{t("changes.board.allRepositories")}</option>{repositoryOptions.map(({ repository }) => <option key={repository.id} value={repository.id}>{repository.display_name}</option>)}</select></label>;
  return <BoardSurface owner={organization.name} title={t("changes.board.organizationTitle", { name: organization.display_name })} description={t("changes.board.organizationDescription")} board={board.data} controls={controls} scopeLabel={t("changes.board.organizationBoard")} scopeControl={scopeControl} />;
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
  const { t } = useTranslation();
  const hasNext = board.page * board.per_page < board.total;
  return <div className="board-page">
    <header className="board-masthead"><div><Link to="/changes" className="board-back">{t("changes.board.back")}</Link><span className="board-eyebrow">{scopeLabel}</span><h1>{title}</h1><p>{description}</p></div>{scopeControl}</header>
    <BoardSummary page={board} />
    <FilterBar filters={controls.filters} update={controls.update} clear={controls.clear} />
    <DiagnosticSummary diagnostics={board.diagnostics} />
    <section id="change-results" className="change-results" aria-labelledby="results-heading" tabIndex={-1}><header><div><span className="board-eyebrow">{t("changes.board.currentProjection")}</span><h2 id="results-heading">{t("changes.board.changes", { count: board.total })}</h2></div><span>{t("changes.board.page", { page: board.page })}</span></header>
      {board.cards.length ? <div className="change-card-grid">{board.cards.map((card) => <ChangeCard key={`${card.repository.id}:${card.change_key}`} card={card} owner={owner} />)}</div> : <BoardState kind="empty"><SearchX aria-hidden="true" /><h2>{t("changes.board.emptyTitle")}</h2><p>{t("changes.board.emptyDescription")}</p><button type="button" className="board-text-button" onClick={controls.clear}>{t("changes.board.resetFilters")}</button></BoardState>}
    </section>
    <nav className="board-pagination" aria-label={t("changes.board.pages")}><button type="button" disabled={board.page === 1} onClick={() => controls.goPage(board.page - 1)}>{t("changes.board.previous")}</button><span><strong>{board.page}</strong><small>{t("changes.board.pageOf", { total: Math.max(1, Math.ceil(board.total / board.per_page)) })}</small></span><button type="button" disabled={!hasNext} onClick={() => controls.goPage(board.page + 1)}>{t("changes.board.next")}</button></nav>
  </div>;
}

function RepositoryBoard({ active }: { active: ActiveRepository }) {
  const { t } = useTranslation();
  const controls = useBoardControls();
  const board = useQuery({ queryKey: ["boards", active.organization.id, active.repository.repository.id, controls.filters], queryFn: ({ signal }) => boardApi.repositoryBoard(active.organization.id, active.repository.repository.id, controls.filters, signal) });
  if (board.isLoading) return <BoardState>{t("changes.board.projecting")}</BoardState>;
  if (isApiProblem(board.error, "not_found") || isApiProblem(board.error, "forbidden")) return <SafeBoardState />;
  if (board.error || !board.data) return <BoardState kind="error"><h1>{t("changes.board.unavailableTitle")}</h1><p>{t("changes.board.unavailableDescription")}</p></BoardState>;
  const scopeControl = active.authenticated ? <Link className="board-text-button" to={`/changes/${active.organization.id}`}>{t("changes.board.organizationBoard")}</Link> : <Link className="board-text-button" to="/login" state={{ returnTo: repositoryChangePath(active) }}>{t("changes.board.signIn")}</Link>;
  return <BoardSurface owner={active.organization.name} title={active.repository.repository.display_name} description={t("changes.board.repositoryDescription", { name: active.repository.repository.name })} board={board.data} controls={controls} scopeLabel={t("changes.board.repositoryBoard")} scopeControl={scopeControl} />;
}

export function RepositoryBoardPage() { return <RepositoryGate>{(active) => <RepositoryBoard active={active} />}</RepositoryGate>; }

export function SafeBoardState() {
  const { t } = useTranslation();
  return <BoardState kind="safe"><h1>{t("changes.board.safeTitle")}</h1><p>{t("changes.board.safeDescription")}</p><Link className="board-state-link" to="/changes">{t("changes.board.returnOrganizations")}</Link></BoardState>;
}
