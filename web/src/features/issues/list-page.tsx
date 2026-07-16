import { useQuery } from "@tanstack/react-query";
import { CircleDot, MessageSquareText, Search } from "lucide-react";
import { Link, useSearchParams } from "react-router-dom";
import { useTranslation } from "react-i18next";
import { LabelChips } from "../../components/labels/label-chips";
import { issueApi } from "./api";
import { IssueLoading, IssueStatus, RepositoryGate, repositoryIssuePath } from "./repository-context";
import type { ActiveRepository } from "./repository-context";

function IssueList({ active }: { active: ActiveRepository }) {
  const { t } = useTranslation();
  const [search, setSearch] = useSearchParams();
  const state = ["open", "closed", "all"].includes(search.get("state") ?? "") ? search.get("state")! : "open";
  const selectedLabels = search.get("labels")?.split(",").filter(Boolean) ?? [];
  const page = Math.max(1, Number(search.get("page") || 1) || 1);
  const owner = active.organization.name;
  const repo = active.repository.repository.name;
  const canContribute = active.authenticated && active.repository.allowed_actions.includes("contribute");
  const labels = useQuery({ queryKey: ["issues", owner, repo, "labels"], queryFn: ({ signal }) => issueApi.listLabels(owner, repo, signal) });
  const issues = useQuery({ queryKey: ["issues", owner, repo, "list", state, selectedLabels.join(","), page], queryFn: ({ signal }) => issueApi.listIssues(owner, repo, { state, labels: selectedLabels, page }, signal) });
  const update = (key: string, value?: string) => { const next = new URLSearchParams(search); if (value) next.set(key, value); else next.delete(key); if (key !== "page") next.delete("page"); setSearch(next); };
  if (issues.isLoading || labels.isLoading) return <IssueLoading />;
  const apiStatus = typeof issues.error === "object" && issues.error && "status" in issues.error ? Number(issues.error.status) : 0;
  if (apiStatus === 403 || apiStatus === 404) return <IssueStatus status={apiStatus} />;
  const toggleLabel = (name: string) => update("labels", selectedLabels.includes(name) ? selectedLabels.filter((item) => item !== name).join(",") : [...selectedLabels, name].join(","));
  const stateLabels = { open: t("issues.list.open"), closed: t("issues.list.closed"), all: t("issues.list.all") };
  return <div className="issue-page"><header className="repo-masthead"><div><Link className="issue-back" to={active.authenticated ? "/issues" : "/"}>{t("issues.list.desk")}</Link><span>/</span><strong>{owner} / {repo}</strong><h1>{t("issues.list.title")}</h1></div>{canContribute ? <Link className="issue-button primary" to={`${repositoryIssuePath(active)}/new`}>{t("issues.list.newIssue")}</Link> : active.authenticated ? null : <Link className="issue-button" to="/login" state={{ returnTo: repositoryIssuePath(active) }}>{t("issues.list.signInToContribute")}</Link>}</header>
    <section className="issue-filters" aria-label={t("issues.list.filters")}><div className="state-tabs" role="group" aria-label={t("issues.list.state")}>{(["open", "closed", "all"] as const).map((option) => <button key={option} className={state === option ? "active" : ""} onClick={() => update("state", option)} type="button">{stateLabels[option]}</button>)}</div><details className="label-filter"><summary><Search aria-hidden="true" size={16} />{t("issues.list.labels")}{selectedLabels.length ? <span>{selectedLabels.length}</span> : null}</summary><fieldset><legend className="sr-only">{t("issues.list.filterByLabels")}</legend>{labels.data?.map((label) => <label key={label.id}><input type="checkbox" checked={selectedLabels.includes(label.name)} onChange={() => toggleLabel(label.name)} /><span>{label.name}</span></label>)}{!labels.data?.length ? <span className="field-note">{t("issues.list.noLabels")}</span> : null}</fieldset></details></section>
    {issues.error ? <div className="issue-inline-error" role="alert">{t("issues.list.loadError")}</div> : null}
    {!issues.data?.length ? <div className="issue-status compact"><span className="empty-orbit" aria-hidden="true" /><h2>{t("issues.list.emptyTitle")}</h2><p>{t("issues.list.emptyDescription", { contributionHint: canContribute ? t("issues.list.contributionHint") : "" })}</p></div> : <ol className="issue-list">{issues.data.map((issue) => <li key={issue.id}><Link to={repositoryIssuePath(active, issue.number)}><span className={`state-dot ${issue.state}`}><CircleDot aria-hidden="true" /></span><span className="issue-list-main"><strong>{issue.title}</strong><span className="issue-meta">{t("issues.list.openedBy", { number: issue.number, date: formatRelative(issue.created_at), actor: displayActor(issue.user) })}</span><LabelChips labels={issue.labels} /></span>{issue.comments ? <span className="comment-count" aria-label={t("issues.list.comments", { count: issue.comments })}><MessageSquareText aria-hidden="true" />{issue.comments}</span> : null}</Link></li>)}</ol>}
    <nav className="pagination" aria-label={t("issues.list.pages")}><button type="button" disabled={page === 1} onClick={() => update("page", String(page - 1))}>{t("issues.list.previous")}</button><span>{t("issues.list.page", { page })}</span><button type="button" disabled={(issues.data?.length ?? 0) < 20} onClick={() => update("page", String(page + 1))}>{t("issues.list.next")}</button></nav>
  </div>;
}

export function IssueListPage() { return <RepositoryGate>{(active) => <IssueList active={active} />}</RepositoryGate>; }

export function formatRelative(value: string) {
  const date = new Date(value);
  return Number.isNaN(date.valueOf()) ? value : new Intl.DateTimeFormat(undefined, { dateStyle: "medium" }).format(date);
}

export function displayActor(user: { login: string; name?: string }) {
  const name = user.name?.trim();
  return name && name !== user.login ? `${name} (@${user.login})` : `@${user.login}`;
}
