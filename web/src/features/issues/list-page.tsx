import { useQuery } from "@tanstack/react-query";
import { CircleDot, MessageSquareText, Search } from "lucide-react";
import { Link, useSearchParams } from "react-router-dom";
import { LabelChips } from "../../components/labels/label-chips";
import { issueApi } from "./api";
import { IssueLoading, IssueStatus, RepositoryGate, repositoryIssuePath } from "./repository-context";
import type { ActiveRepository } from "./repository-context";

function IssueList({ active }: { active: ActiveRepository }) {
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
  return <div className="issue-page"><header className="repo-masthead"><div><Link className="issue-back" to={active.authenticated ? "/issues" : "/"}>Issue desk</Link><span>/</span><strong>{owner} / {repo}</strong><h1>Issues</h1></div>{canContribute ? <Link className="issue-button primary" to={`${repositoryIssuePath(active)}/new`}>New issue</Link> : active.authenticated ? null : <Link className="issue-button" to="/login" state={{ returnTo: repositoryIssuePath(active) }}>Sign in to contribute</Link>}</header>
    <section className="issue-filters" aria-label="Issue filters"><div className="state-tabs" role="group" aria-label="State">{["open", "closed", "all"].map((option) => <button key={option} className={state === option ? "active" : ""} onClick={() => update("state", option)} type="button">{option}</button>)}</div><details className="label-filter"><summary><Search aria-hidden="true" size={16} />Labels{selectedLabels.length ? <span>{selectedLabels.length}</span> : null}</summary><fieldset><legend className="sr-only">Filter by labels</legend>{labels.data?.map((label) => <label key={label.id}><input type="checkbox" checked={selectedLabels.includes(label.name)} onChange={() => toggleLabel(label.name)} /><span>{label.name}</span></label>)}{!labels.data?.length ? <span className="field-note">No labels</span> : null}</fieldset></details></section>
    {issues.error ? <div className="issue-inline-error" role="alert">Issues could not be loaded. Try again.</div> : null}
    {!issues.data?.length ? <div className="issue-status compact"><span className="empty-orbit" aria-hidden="true" /><h2>No issues match this view</h2><p>Adjust the state or label filter{canContribute ? ", or open the first conversation" : ""}.</p></div> : <ol className="issue-list">{issues.data.map((issue) => <li key={issue.id}><Link to={repositoryIssuePath(active, issue.number)}><span className={`state-dot ${issue.state}`}><CircleDot aria-hidden="true" /></span><span className="issue-list-main"><strong>{issue.title}</strong><span className="issue-meta">#{issue.number} opened {formatRelative(issue.created_at)} by @{issue.user.login}</span><LabelChips labels={issue.labels} /></span>{issue.comments ? <span className="comment-count" aria-label={`${issue.comments} comments`}><MessageSquareText aria-hidden="true" />{issue.comments}</span> : null}</Link></li>)}</ol>}
    <nav className="pagination" aria-label="Issue pages"><button type="button" disabled={page === 1} onClick={() => update("page", String(page - 1))}>Previous</button><span>Page {page}</span><button type="button" disabled={(issues.data?.length ?? 0) < 20} onClick={() => update("page", String(page + 1))}>Next</button></nav>
  </div>;
}

export function IssueListPage() { return <RepositoryGate>{(active) => <IssueList active={active} />}</RepositoryGate>; }

export function formatRelative(value: string) {
  const date = new Date(value);
  return Number.isNaN(date.valueOf()) ? value : new Intl.DateTimeFormat(undefined, { dateStyle: "medium" }).format(date);
}
