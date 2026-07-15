import { useMutation, useQuery, useQueryClient } from "@tanstack/react-query";
import { CircleDot, LockKeyhole, Pencil, RotateCcw } from "lucide-react";
import { useEffect, useRef, useState } from "react";
import { Link, useLocation, useParams } from "react-router-dom";
import { LabelChips, LabelSelector } from "../../components/labels/label-chips";
import { MarkdownView } from "../../components/markdown/markdown-view";
import { ReactionPicker } from "../../components/reactions/reaction-picker";
import { useCurrentContext } from "../../auth/session";
import { issueApi } from "./api";
import { CommentEditor, IssueEditor } from "./issue-editor";
import { formatRelative } from "./list-page";
import { CodeChangeList } from "../changes/relationships";
import { IssueLoading, IssueStatus, MutationProblem, RepositoryGate, repositoryIssuePath } from "./repository-context";
import type { ActiveRepository } from "./repository-context";
import type { IssueComment } from "./types";
import { Avatar } from "../../app/avatar";
import { useTranslation } from "react-i18next";

const commentAnchorPattern = /^#issuecomment-[1-9]\d*$/;

function useCommentAnchor({ comments, hash, owner, repo, number }: { comments: IssueComment[] | undefined; hash: string; owner: string; repo: string; number: number }) {
  const handledAnchor = useRef("");

  useEffect(() => {
    if (!commentAnchorPattern.test(hash)) {
      handledAnchor.current = "";
      document.querySelector('[data-comment-anchor-active="true"]')?.removeAttribute("data-comment-anchor-active");
      return;
    }
    if (!comments) return;
    const anchorKey = `${owner}/${repo}/issues/${number}${hash}`;
    if (handledAnchor.current === anchorKey) return;

    const frame = window.requestAnimationFrame(() => {
      const target = document.getElementById(hash.slice(1));
      if (!target) return;
      handledAnchor.current = anchorKey;
      document.querySelector('[data-comment-anchor-active="true"]')?.removeAttribute("data-comment-anchor-active");
      target.setAttribute("data-comment-anchor-active", "true");
      target.scrollIntoView({ block: "start" });
      target.focus({ preventScroll: true });
    });
    return () => window.cancelAnimationFrame(frame);
  }, [comments, hash, number, owner, repo]);
}

function EditableComment({ comment, owner, repo, currentLogin, canContribute, canTriage }: { comment: IssueComment; owner: string; repo: string; currentLogin: string; canContribute: boolean; canTriage: boolean }) {
  const { t } = useTranslation();
  const [editing, setEditing] = useState(false);
  const queryClient = useQueryClient();
  const mutation = useMutation({ mutationFn: (body: string) => issueApi.updateComment(owner, repo, comment.id, body), onSuccess: async () => { setEditing(false); await queryClient.invalidateQueries({ queryKey: ["issues", owner, repo] }); } });
  const canEdit = canTriage || (canContribute && comment.user.login === currentLogin);
  return <article className="timeline-card" id={`issuecomment-${comment.id}`} tabIndex={-1}><header><Avatar login={comment.user.login} src={comment.user.avatar_url} /><span><strong>@{comment.user.login}</strong><small>{t("issues.detail.commented", { date: formatRelative(comment.created_at) })}</small></span>{canEdit && !editing ? <button className="quiet-action" type="button" onClick={() => setEditing(true)}><Pencil aria-hidden="true" />{t("issues.detail.edit")}</button> : null}</header><div className="timeline-body">{editing ? <CommentEditor initial={comment.body} submitLabel={t("issues.detail.saveComment")} pending={mutation.isPending} error={mutation.error} onCancel={() => setEditing(false)} onSubmit={(body) => mutation.mutate(body)} /> : <MarkdownView source={comment.body} />}</div>{!editing ? <ReactionPicker owner={owner} repo={repo} commentId={comment.id} summary={comment.reactions} currentLogin={currentLogin} readOnly={!canContribute} /> : null}</article>;
}

export function IssueDetail({ active }: { active: ActiveRepository }) {
  const { t } = useTranslation();
  const { number: rawNumber = "" } = useParams();
  const location = useLocation();
  const number = Number(rawNumber);
  const owner = active.organization.name;
  const repo = active.repository.repository.name;
  const canContribute = active.authenticated && active.repository.allowed_actions.includes("contribute");
  const canTriage = active.authenticated && active.repository.allowed_actions.includes("triage");
  const current = useCurrentContext(active.authenticated);
  const queryClient = useQueryClient();
  const [editing, setEditing] = useState(false);
  const issue = useQuery({ queryKey: ["issues", owner, repo, number], queryFn: ({ signal }) => issueApi.getIssue(owner, repo, number, signal), enabled: Number.isInteger(number) && number > 0 });
  const relationships = useQuery({ queryKey: ["issues", owner, repo, number, "relationships"], queryFn: ({ signal }) => issueApi.getRelationships(owner, repo, number, signal), enabled: issue.isSuccess });
  const comments = useQuery({ queryKey: ["issues", owner, repo, number, "comments"], queryFn: ({ signal }) => issueApi.listComments(owner, repo, number, signal), enabled: issue.isSuccess });
  useCommentAnchor({ comments: comments.data, hash: location.hash, owner, repo, number });
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
  if (issue.isLoading) return <IssueLoading label={t("issues.detail.openingTimeline")} />;
  const status = typeof issue.error === "object" && issue.error && "status" in issue.error ? Number(issue.error.status) : 0;
  if (!issue.data) return <IssueStatus status={status === 403 ? 403 : 404} />;
  const item = issue.data;
  return <div className="issue-page"><header className="detail-header"><div className="detail-title"><Link className="issue-back" to={repositoryIssuePath(active)}>← {owner} / {repo}</Link><h1>{item.title} <span>#{item.number}</span></h1><div className="detail-state-row"><span className={`state-badge ${item.state}`}><CircleDot aria-hidden="true" />{t(item.state === "open" ? "issues.detail.stateOpen" : "issues.detail.stateClosed")}</span><span>{t("issues.detail.openedOn", { login: item.user.login, date: formatRelative(item.created_at) })}</span><span>{t("issues.detail.comments", { count: item.comments })}</span></div></div>{canTriage ? <div className="header-actions"><button className="issue-button" type="button" onClick={() => setEditing((value) => !value)}><Pencil aria-hidden="true" />{t(editing ? "issues.detail.cancelEdit" : "issues.detail.edit")}</button><button className="issue-button" type="button" disabled={updateState.isPending} onClick={() => updateState.mutate(item.state === "open" ? "closed" : "open")}><RotateCcw aria-hidden="true" />{t(item.state === "open" ? "issues.detail.close" : "issues.detail.reopen")}</button></div> : null}</header>
    <MutationProblem error={updateState.error} />
    <div className="detail-grid"><div className="timeline"><article className="timeline-card issue-origin"><header><Avatar login={item.user.login} src={item.user.avatar_url} tone="coral" /><span><strong>@{item.user.login}</strong><small>{t("issues.detail.openingNote")}</small></span></header><div className="timeline-body">{editing ? <IssueEditor initial={{ title: item.title, body: item.body, labels: item.labels.map((label) => label.name) }} labels={labels.data ?? []} submitLabel={t("issues.detail.saveIssue")} pending={updateIssue.isPending} error={updateIssue.error} onCancel={() => setEditing(false)} onSubmit={(draft) => updateIssue.mutate(draft)} /> : item.body ? <MarkdownView source={item.body} /> : <p className="issue-empty-copy">{t("issues.detail.noDescription")}</p>}</div></article>
      {comments.isLoading ? <IssueLoading label={t("issues.detail.loadingConversation")} /> : comments.data?.map((comment) => <EditableComment key={comment.id} comment={comment} owner={owner} repo={repo} currentLogin={current.data?.user.login ?? ""} canContribute={canContribute} canTriage={canTriage} />)}
      {canContribute ? <section className="new-comment" aria-labelledby="new-comment-heading"><h2 id="new-comment-heading">{t("issues.detail.continueConversation")}</h2><CommentEditor key={comments.data?.length ?? 0} pending={createComment.isPending} error={createComment.error} onSubmit={(body) => createComment.mutate(body)} /></section> : <section className="new-comment read-only-note" aria-label={t("issues.detail.readOnlyAccess")}><h2>{t("issues.detail.readOnlyConversation")}</h2><p>{t(active.authenticated ? "issues.detail.noCommentPermission" : "issues.detail.signInToComment")}</p>{active.authenticated ? null : <Link className="issue-button" to="/login" state={{ returnTo: repositoryIssuePath(active, item.number) }}>{t("issues.detail.signIn")}</Link>}</section>}</div>
      <aside className="issue-sidebar" aria-label={t("issues.detail.metadata")}><section><span className="issue-kicker purple">{t("issues.detail.labels")}</span><LabelChips labels={item.labels} />{canTriage ? <details><summary>{t("issues.detail.manageLabels")}</summary><LabelSelector labels={labels.data ?? []} selected={item.labels.map((label) => label.name)} disabled={replaceLabels.isPending} onChange={(names) => replaceLabels.mutate(names)} /></details> : null}<MutationProblem error={replaceLabels.error} /></section><section className="issue-relationships" aria-labelledby="issue-relationships-heading"><span className="issue-kicker coral" id="issue-relationships-heading">{t("issues.detail.codeChanges")}</span>{relationships.isLoading ? <p className="relationship-status" role="status">{t("issues.detail.loadingDelivery")}</p> : relationships.error ? <p className="relationship-status relationship-error" role="alert">{t("issues.detail.deliveryUnavailable")}</p> : <CodeChangeList relationships={relationships.data?.relationships ?? []} />}</section><section><span className="issue-kicker">{t("issues.detail.authority")}</span><p><LockKeyhole aria-hidden="true" />{t(`common.permission.${active.repository.effective_permission}`)}</p><small>{t(active.authenticated ? "issues.detail.serverChecks" : "issues.detail.publicRead")}</small></section></aside></div>
  </div>;
}

export function IssueDetailPage() { return <RepositoryGate>{(active) => <IssueDetail active={active} />}</RepositoryGate>; }
