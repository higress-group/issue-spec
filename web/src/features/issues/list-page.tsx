import { useQuery } from "@tanstack/react-query";
import { CircleDot, FileText, MessageSquareText, Search, X } from "lucide-react";
import { useRef, useState, type FormEvent } from "react";
import { Link, useSearchParams } from "react-router-dom";
import { Trans, useTranslation } from "react-i18next";
import { useMeta } from "../../auth/session";
import { LabelChips } from "../../components/labels/label-chips";
import { RepositorySubscriptionControl } from "../../repos/repository-subscription-control";
import "../../repos/repository-notifications.css";
import { issueApi } from "./api";
import { IssueLoading, IssueStatus, RepositoryGate, repositoryIssuePath } from "./repository-context";
import type { ActiveRepository } from "./repository-context";
import { PreciseRelativeTime, useSecondClock } from "./relative-time";
import type { FullIssueSearchPage } from "./types";

export function IssueList({ active }: { active: ActiveRepository }) {
  const { t } = useTranslation();
  const now = useSecondClock();
  const meta = useMeta();
  const [focusedIssueId, setFocusedIssueId] = useState<number | null>(null);
  const [search, setSearch] = useSearchParams();
  const queryInput = useRef<HTMLInputElement>(null);
  const query = meta.data?.features.search ? (search.get("q") ?? "").trim() : "";
  const state = ["open", "closed", "all"].includes(search.get("state") ?? "") ? search.get("state")! : "open";
  const selectedLabels = search.get("labels")?.split(",").filter(Boolean) ?? [];
  const page = Math.max(1, Number(search.get("page") || 1) || 1);
  const owner = active.organization.name;
  const repo = active.repository.repository.name;
  const canRead = active.authenticated && active.repository.allowed_actions.includes("read");
  const canContribute = active.authenticated && active.repository.allowed_actions.includes("contribute");
  const labels = useQuery({ queryKey: ["issues", owner, repo, "labels"], queryFn: ({ signal }) => issueApi.listLabels(owner, repo, signal) });
  const issues = useQuery({ queryKey: ["issues", owner, repo, "list", state, selectedLabels.join(","), page],
    queryFn: ({ signal }) => issueApi.listIssues(owner, repo, { state, labels: selectedLabels, page }, signal), enabled: !query });
  const fullSearch = useQuery({ queryKey: ["issues", owner, repo, "full-search", query, state, selectedLabels.join(","), page],
    queryFn: ({ signal }) => issueApi.searchIssues(owner, repo, { query, state, labels: selectedLabels, page }, signal), enabled: !!query });
  const update = (key: string, value?: string) => { const next = new URLSearchParams(search); if (value) next.set(key, value); else next.delete(key); if (key !== "page") next.delete("page"); setSearch(next); };
  const submitSearch = (event: FormEvent) => { event.preventDefault(); update("q", queryInput.current?.value.trim()); };
  const activeQuery = query ? fullSearch : issues;
  if (activeQuery.isLoading || labels.isLoading) return <IssueLoading />;
  const apiStatus = typeof activeQuery.error === "object" && activeQuery.error && "status" in activeQuery.error ? Number(activeQuery.error.status) : 0;
  if (apiStatus === 403 || apiStatus === 404) return <IssueStatus status={apiStatus} />;
  const toggleLabel = (name: string) => update("labels", selectedLabels.includes(name) ? selectedLabels.filter((item) => item !== name).join(",") : [...selectedLabels, name].join(","));
  const stateLabels = { open: t("issues.list.open"), closed: t("issues.list.closed"), all: t("issues.list.all") };
  return <div className="issue-page"><header className="repo-masthead"><div><Link className="issue-back" to={active.authenticated ? "/issues" : "/"}>{t("issues.list.desk")}</Link><span>/</span><strong>{owner} / {repo}</strong><h1>{t("issues.list.title")}</h1></div><div className="header-actions">{canRead ? <RepositorySubscriptionControl orgId={active.organization.id} repoId={active.repository.repository.id} /> : null}{canContribute ? <Link className="issue-button primary" to={`${repositoryIssuePath(active)}/new`}>{t("issues.list.newIssue")}</Link> : active.authenticated ? null : <Link className="issue-button" to="/login" state={{ returnTo: repositoryIssuePath(active) }}>{t("issues.list.signInToContribute")}</Link>}</div></header>
    {meta.data?.features.search ? <form className="issue-list-search" role="search" aria-label={t("issues.list.searchAria")} onSubmit={submitSearch}><label><span className="sr-only">{t("issues.list.searchLabel")}</span><Search aria-hidden="true" /><input key={query} ref={queryInput} type="search" aria-label={t("issues.list.searchLabel")} defaultValue={query} maxLength={256} placeholder={t("issues.list.searchPlaceholder")} /><button type="submit">{t("issues.list.searchSubmit")}</button>{query ? <button className="clear-search" type="button" onClick={() => update("q")}><X aria-hidden="true" />{t("issues.list.searchClear")}</button> : null}</label><p>{t("issues.list.searchHelp")}</p></form> : null}
    <section className="issue-filters" aria-label={t("issues.list.filters")}><div className="state-tabs" role="group" aria-label={t("issues.list.state")}>{(["open", "closed", "all"] as const).map((option) => <button key={option} className={state === option ? "active" : ""} onClick={() => update("state", option)} type="button">{stateLabels[option]}</button>)}</div><details className="label-filter"><summary><Search aria-hidden="true" size={16} />{t("issues.list.labels")}{selectedLabels.length ? <span>{selectedLabels.length}</span> : null}</summary><fieldset><legend className="sr-only">{t("issues.list.filterByLabels")}</legend>{labels.data?.map((label) => <label key={label.id}><input type="checkbox" checked={selectedLabels.includes(label.name)} onChange={() => toggleLabel(label.name)} /><span>{label.name}</span></label>)}{!labels.data?.length ? <span className="field-note">{t("issues.list.noLabels")}</span> : null}</fieldset></details></section>
    {activeQuery.error ? <div className="issue-inline-error" role="alert">{query ? t("issues.list.searchError") : t("issues.list.loadError")}</div> : null}
    {query ? activeQuery.error ? null : <FullSearchResults active={active} page={fullSearch.data} now={now} /> : !issues.data?.length ? <div className="issue-status compact"><span className="empty-orbit" aria-hidden="true" /><h2>{t("issues.list.emptyTitle")}</h2><p>{t("issues.list.emptyDescription", { contributionHint: canContribute ? t("issues.list.contributionHint") : "" })}</p></div> : <ol className="issue-list">{issues.data.map((issue) => <li key={issue.id}><Link to={repositoryIssuePath(active, issue.number)} onFocus={() => setFocusedIssueId(issue.id)} onBlur={() => setFocusedIssueId((current) => current === issue.id ? null : current)}><span className={`state-dot ${issue.state}`}><CircleDot aria-hidden="true" /></span><span className="issue-list-main"><strong>{issue.title}</strong><span className="issue-meta"><Trans i18nKey="issues.list.openedByRelative" values={{ number: issue.number, actor: displayActor(issue.user) }} components={{ time: <PreciseRelativeTime value={issue.created_at} now={now} focusable={false} disclosed={focusedIssueId === issue.id} /> }} /></span><LabelChips labels={issue.labels} /></span>{issue.comments ? <span className="comment-count" aria-label={t("issues.list.comments", { count: issue.comments })}><MessageSquareText aria-hidden="true" />{issue.comments}</span> : null}</Link></li>)}</ol>}
    <nav className="pagination" aria-label={t("issues.list.pages")}><button type="button" disabled={page === 1} onClick={() => update("page", String(page - 1))}>{t("issues.list.previous")}</button><span>{t("issues.list.page", { page })}</span><button type="button" disabled={query ? !fullSearch.data?.has_next : (issues.data?.length ?? 0) < 20} onClick={() => update("page", String(page + 1))}>{t("issues.list.next")}</button></nav>
  </div>;
}

function FullSearchResults({ active, page, now }: { active: ActiveRepository; page?: FullIssueSearchPage; now: number }) {
  const { t } = useTranslation();
  if (!page?.items.length) return <div className="issue-status compact"><span className="empty-orbit" aria-hidden="true" /><h2>{t("issues.list.searchEmptyTitle")}</h2><p>{t("issues.list.searchEmptyDescription")}</p></div>;
  return <><div className="issue-search-summary" role="status"><strong>{t("issues.list.searchResults", { count: page.total })}</strong><span>{t("issues.list.searchResultsHelp")}</span></div><ol className="issue-list issue-search-results">{page.items.map((issue) => <li key={issue.id}><Link to={repositoryIssuePath(active, issue.number)}><span className={`state-dot ${issue.state}`}><CircleDot aria-hidden="true" /></span><span className="issue-list-main"><strong>{issue.title}</strong><span className="issue-meta"><Trans i18nKey="issues.list.updatedRelative" values={{ number: issue.number }} components={{ time: <PreciseRelativeTime value={issue.updated_at} now={now} focusable={false} /> }} /></span><span className="issue-search-matches">{issue.matches.map((match, index) => <span className="issue-search-match" key={match.comment_id ?? `${match.source}-${index}`}><span>{match.source === "comment" ? <MessageSquareText aria-hidden="true" /> : <FileText aria-hidden="true" />}{t(`issues.list.match.${match.source}`)}</span><span>{match.excerpt}</span></span>)}</span></span></Link></li>)}</ol></>;
}

export function IssueListPage() { return <RepositoryGate>{(active) => <IssueList active={active} />}</RepositoryGate>; }

export function displayActor(user: { login: string; name?: string }) {
  const name = user.name?.trim();
  return name && name !== user.login ? `${name} (@${user.login})` : `@${user.login}`;
}
