import { useMutation, useQuery, useQueryClient } from "@tanstack/react-query";
import { CircleDot, LockKeyhole, Pencil, RotateCcw } from "lucide-react";
import { useState } from "react";
import { Link, useParams } from "react-router-dom";
import { LabelChips, LabelSelector } from "../../components/labels/label-chips";
import { MarkdownView } from "../../components/markdown/markdown-view";
import { ReactionPicker } from "../../components/reactions/reaction-picker";
import { useCurrentContext } from "../../auth/session";
import { issueApi } from "./api";
import { CommentEditor, IssueEditor } from "./issue-editor";
import { formatRelative } from "./list-page";
import { IssueLoading, IssueStatus, MutationProblem, RepositoryGate } from "./repository-context";
import type { ActiveRepository } from "./repository-context";
import type { IssueComment } from "./types";

function EditableComment({ comment, owner, repo, currentLogin }: { comment: IssueComment; owner: string; repo: string; currentLogin: string }) {
  const [editing, setEditing] = useState(false);
  const queryClient = useQueryClient();
  const mutation = useMutation({ mutationFn: (body: string) => issueApi.updateComment(owner, repo, comment.id, body), onSuccess: async () => { setEditing(false); await queryClient.invalidateQueries({ queryKey: ["issues", owner, repo] }); } });
  return <article className="timeline-card" id={`issuecomment-${comment.id}`}><header><span className="avatar" aria-hidden="true">{comment.user.login.slice(0, 2).toUpperCase()}</span><span><strong>@{comment.user.login}</strong><small>commented {formatRelative(comment.created_at)}</small></span>{comment.user.login === currentLogin && !editing ? <button className="quiet-action" type="button" onClick={() => setEditing(true)}><Pencil aria-hidden="true" />Edit</button> : null}</header><div className="timeline-body">{editing ? <CommentEditor initial={comment.body} submitLabel="Save comment" pending={mutation.isPending} error={mutation.error} onCancel={() => setEditing(false)} onSubmit={(body) => mutation.mutate(body)} /> : <MarkdownView source={comment.body} />}</div>{!editing ? <ReactionPicker owner={owner} repo={repo} commentId={comment.id} summary={comment.reactions} currentLogin={currentLogin} /> : null}</article>;
}

function IssueDetail({ active }: { active: ActiveRepository }) {
  const { number: rawNumber = "" } = useParams();
  const number = Number(rawNumber);
  const owner = active.organization.name;
  const repo = active.repository.repository.name;
  const current = useCurrentContext();
  const queryClient = useQueryClient();
  const [editing, setEditing] = useState(false);
  const issue = useQuery({ queryKey: ["issues", owner, repo, number], queryFn: ({ signal }) => issueApi.getIssue(owner, repo, number, signal), enabled: Number.isInteger(number) && number > 0 });
  const comments = useQuery({ queryKey: ["issues", owner, repo, number, "comments"], queryFn: ({ signal }) => issueApi.listComments(owner, repo, number, signal), enabled: issue.isSuccess });
  const labels = useQuery({ queryKey: ["issues", owner, repo, "labels"], queryFn: ({ signal }) => issueApi.listLabels(owner, repo, signal) });
  const updateIssue = useMutation({ mutationFn: async (draft: { title: string; body: string; labels: string[] }) => {
    const updated = await issueApi.updateIssue(owner, repo, number, { title: draft.title, body: draft.body });
    const before = item.labels.map((label) => label.name).sort().join("\n");
    const after = [...draft.labels].sort().join("\n");
    if (before !== after) await issueApi.replaceLabels(owner, repo, number, draft.labels);
    return updated;
  }, onSuccess: async () => { setEditing(false); await queryClient.invalidateQueries({ queryKey: ["issues", owner, repo] }); } });
  const updateState = useMutation({ mutationFn: (state: "open" | "closed") => issueApi.updateIssue(owner, repo, number, { state }), onSuccess: async () => queryClient.invalidateQueries({ queryKey: ["issues", owner, repo] }) });
  const replaceLabels = useMutation({ mutationFn: (names: string[]) => issueApi.replaceLabels(owner, repo, number, names), onSuccess: async () => queryClient.invalidateQueries({ queryKey: ["issues", owner, repo] }) });
  const createComment = useMutation({ mutationFn: (body: string) => issueApi.createComment(owner, repo, number, body), onSuccess: async () => queryClient.invalidateQueries({ queryKey: ["issues", owner, repo] }) });
  if (!Number.isInteger(number) || number < 1) return <IssueStatus status={404} />;
  if (issue.isLoading) return <IssueLoading label="Opening issue timeline" />;
  const status = typeof issue.error === "object" && issue.error && "status" in issue.error ? Number(issue.error.status) : 0;
  if (!issue.data) return <IssueStatus status={status === 403 ? 403 : 404} />;
  const item = issue.data;
  return <div className="issue-page"><header className="detail-header"><div className="detail-title"><Link className="issue-back" to={`/issues/${active.organization.id}/${active.repository.repository.id}`}>← {owner} / {repo}</Link><h1>{item.title} <span>#{item.number}</span></h1><div className="detail-state-row"><span className={`state-badge ${item.state}`}><CircleDot aria-hidden="true" />{item.state}</span><span>@{item.user.login} opened this on {formatRelative(item.created_at)}</span><span>· {item.comments} {item.comments === 1 ? "comment" : "comments"}</span></div></div><div className="header-actions"><button className="issue-button" type="button" onClick={() => setEditing((value) => !value)}><Pencil aria-hidden="true" />{editing ? "Cancel edit" : "Edit"}</button><button className="issue-button" type="button" disabled={updateState.isPending} onClick={() => updateState.mutate(item.state === "open" ? "closed" : "open")}><RotateCcw aria-hidden="true" />{item.state === "open" ? "Close" : "Reopen"}</button></div></header>
    <MutationProblem error={updateState.error} />
    <div className="detail-grid"><div className="timeline"><article className="timeline-card issue-origin"><header><span className="avatar coral-avatar" aria-hidden="true">{item.user.login.slice(0, 2).toUpperCase()}</span><span><strong>@{item.user.login}</strong><small>wrote the opening note</small></span></header><div className="timeline-body">{editing ? <IssueEditor initial={{ title: item.title, body: item.body, labels: item.labels.map((label) => label.name) }} labels={labels.data ?? []} submitLabel="Save issue" pending={updateIssue.isPending} error={updateIssue.error} onCancel={() => setEditing(false)} onSubmit={(draft) => updateIssue.mutate(draft)} /> : item.body ? <MarkdownView source={item.body} /> : <p className="issue-empty-copy">No description was provided.</p>}</div></article>
      {comments.isLoading ? <IssueLoading label="Loading conversation" /> : comments.data?.map((comment) => <EditableComment key={comment.id} comment={comment} owner={owner} repo={repo} currentLogin={current.data?.user.login ?? ""} />)}
      <section className="new-comment" aria-labelledby="new-comment-heading"><h2 id="new-comment-heading">Continue the conversation</h2><CommentEditor key={comments.data?.length ?? 0} pending={createComment.isPending} error={createComment.error} onSubmit={(body) => createComment.mutate(body)} /></section></div>
      <aside className="issue-sidebar" aria-label="Issue metadata"><section><span className="issue-kicker purple">Labels</span><LabelChips labels={item.labels} /><details><summary>Manage labels</summary><LabelSelector labels={labels.data ?? []} selected={item.labels.map((label) => label.name)} disabled={replaceLabels.isPending} onChange={(names) => replaceLabels.mutate(names)} /></details><MutationProblem error={replaceLabels.error} /></section><section><span className="issue-kicker">Authority</span><p><LockKeyhole aria-hidden="true" />{active.repository.effective_permission}</p><small>Changes are checked again by the server.</small></section></aside></div>
  </div>;
}

export function IssueDetailPage() { return <RepositoryGate>{(active) => <IssueDetail active={active} />}</RepositoryGate>; }
