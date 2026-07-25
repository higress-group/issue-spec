import { useMutation, useQuery, useQueryClient } from "@tanstack/react-query";
import { Check, CircleDot, Link2, LockKeyhole, Pencil, RotateCcw } from "lucide-react";
import { useCallback, useEffect, useMemo, useRef, useState } from "react";
import { Link, useLocation, useParams } from "react-router-dom";
import { LabelChips, LabelSelector } from "../../components/labels/label-chips";
import { hasExecutableHtmlPreview, MarkdownView } from "../../components/markdown/markdown-view";
import { ReactionPicker } from "../../components/reactions/reaction-picker";
import { useCurrentContext } from "../../auth/session";
import { issueApi } from "./api";
import { CommentEditor, IssueEditor } from "./issue-editor";
import { displayActor } from "./list-page";
import { CodeChangeList } from "../changes/relationships";
import { IssueLoading, IssueStatus, MutationProblem, RepositoryGate, repositoryIssuePath } from "./repository-context";
import type { ActiveRepository } from "./repository-context";
import type { IssueComment } from "./types";
import { Avatar } from "../../app/avatar";
import { Trans, useTranslation } from "react-i18next";
import { copyText } from "../../lib/clipboard";
import { isLaterTimestamp, PreciseRelativeTime, useSecondClock } from "./relative-time";
import { useMeta } from "../../auth/session";
import { createHtmlPreviewActivity, type HtmlPreviewContext } from "../../components/markdown/html-preview";
import { previewAnswerRequest, type PreviewAnswerIntent } from "../../components/markdown/html-preview-message";
import { QuestionAnswerPanel, questionCommentId } from "./question-answer-panel";
import type { QuestionAuthority } from "./types";

const commentAnchorPattern = /^#issuecomment-[1-9]\d*$/;
const htmlPreviewFencePattern = /^ {0,3}(?:`{3,}|~{3,})html-preview(?:[ \t]|$)/m;
type PreviewSource = { kind: "issue" } | { kind: "comment"; commentId: number };
type PreviewContextFactory = (source: PreviewSource) => HtmlPreviewContext | undefined;
type DefaultPreviewSource = "issue" | `comment:${number}` | "";

function hasHtmlPreview(source: string) {
  return htmlPreviewFencePattern.test(source);
}

function selectDefaultPreviewSource(issueBody: string, comments: IssueComment[] | undefined, hash: string): DefaultPreviewSource {
  const executable = (source: string) => hasHtmlPreview(source) && hasExecutableHtmlPreview(source);
  const anchoredComment = commentAnchorPattern.test(hash) ? Number(hash.slice("#issuecomment-".length)) : 0;
  if (anchoredComment && !comments) return "";
  if (anchoredComment) {
    const target = comments?.find((comment) => comment.id === anchoredComment);
    if (target && executable(target.body)) return `comment:${target.id}`;
  }
  if (executable(issueBody)) return "issue";
  const comment = comments?.find((candidate) => executable(candidate.body));
  return comment ? `comment:${comment.id}` : "";
}

function AuthorName({ user }: { user: IssueComment["user"] }) {
  const content = <><strong>{user.name || `@${user.login}`}</strong>{user.name ? <small>@{user.login}</small> : null}</>;
  return user.html_url
    ? <Link className="author-link" to={`/users/${encodeURIComponent(user.login)}`}>{content}</Link>
    : <span className="author-link">{content}</span>;
}

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

function answerSelection(authority: QuestionAuthority, intent: PreviewAnswerIntent) {
  const question = authority.question;
  if (question.id !== intent.questionId || question.choice_model.mode !== intent.mode) return null;
  if (intent.custom.trim()) {
    if (!question.choice_model.allow_custom || intent.optionIds.length) return null;
    return { labels: [] as string[], custom: intent.custom };
  }
  const options = new Map(question.choice_model.options.map((option) => [option.id, option]));
  const selected = intent.optionIds.map((id) => options.get(id));
  if (selected.some((option) => !option) || !selected.length) return null;
  if (question.choice_model.mode === "single" && selected.length !== 1) return null;
  return { labels: selected.map((option) => option?.label ?? ""), custom: "" };
}

function AnswerConfirmation({ owner, repo, number, intent, onClose }: {
  owner: string;
  repo: string;
  number: number;
  intent: PreviewAnswerIntent;
  onClose: () => void;
}) {
  const { t } = useTranslation();
  const queryClient = useQueryClient();
  const dialogRef = useRef<HTMLDialogElement>(null);
  useEffect(() => {
    const dialog = dialogRef.current;
    if (!dialog || dialog.open) return;
    // jsdom 等环境未实现 showModal 时退化为非模态 open，保持内容可见。
    if (typeof dialog.showModal === "function") dialog.showModal();
    else dialog.open = true;
  }, []);
  const questionQueryKey = ["issues", owner, repo, number, "question", intent.questionId] as const;
  const question = useQuery({
    queryKey: questionQueryKey,
    queryFn: ({ signal }) => issueApi.getQuestion(owner, repo, number, intent.questionId, signal),
    staleTime: 0,
    refetchOnMount: "always",
  });
  const authority = question.isFetching || question.error ? undefined : question.data;
  const selection = authority ? answerSelection(authority, intent) : null;
  const mutation = useMutation({
    mutationFn: async () => {
      if (!authority || !selection) throw new Error("Invalid answer intent");
      const confirmedDigest = authority.body_digest;
      const latest = await question.refetch({ throwOnError: true });
      if (!latest.data || latest.data.body_digest !== confirmedDigest ||
          latest.data.representation_version !== authority.representation_version ||
          !answerSelection(latest.data, intent)) throw new Error("QUESTION authority changed");
      return issueApi.createAnswer(owner, repo, number, previewAnswerRequest(intent, latest.data.body_digest));
    },
    onSuccess: async () => {
      await queryClient.invalidateQueries({ queryKey: ["issues", owner, repo, number, "comments"] });
      await queryClient.invalidateQueries({ queryKey: questionQueryKey });
      onClose();
    },
  });
  return <dialog
    ref={dialogRef}
    className="answer-confirmation-dialog"
    aria-labelledby="answer-confirmation-heading"
    onCancel={onClose}
    onClick={(event) => { if (event.target === dialogRef.current) onClose(); }}
  >
    <section className="answer-confirmation">
    <div>
      <span className="issue-kicker purple">{t("issues.answer.trustedConfirmation")}</span>
      <h2 id="answer-confirmation-heading">{t("issues.answer.title")}</h2>
    </div>
    {question.isFetching ? <p role="status">{t("issues.answer.loading")}</p> : question.error ? <p role="alert">{t("issues.answer.unavailable")}</p> : authority && selection ? <>
      <p className="answer-question">{authority.question.question}</p>
      {selection.custom ? <dl><dt>{t("issues.answer.custom")}</dt><dd>{selection.custom}</dd></dl> :
        <dl><dt>{t("issues.answer.selection")}</dt><dd>{selection.labels.join(", ")}</dd></dl>}
      <p className="answer-authority">{t("issues.answer.authority")}</p>
    </> : <p role="alert">{t("issues.answer.invalid")}</p>}
    {mutation.error ? <p role="alert">{t("issues.answer.failed")}</p> : null}
    <div className="answer-confirmation-actions">
      <button className="issue-button" type="button" onClick={onClose}>{t("issues.answer.cancel")}</button>
      <button className="issue-button primary" type="button" disabled={!selection || question.isFetching || mutation.isPending} onClick={() => mutation.mutate()}>
        {t(mutation.isPending ? "issues.answer.submitting" : "issues.answer.confirm")}
      </button>
    </div>
    </section>
  </dialog>;
}

function EditableComment({ comment, owner, repo, number, issuePath, currentLogin, canContribute, canTriage, now, createPreviewContext, renderFirstPreview, questionPanel }: { comment: IssueComment; owner: string; repo: string; number: number; issuePath: string; currentLogin: string; canContribute: boolean; canTriage: boolean; now: number; createPreviewContext: PreviewContextFactory; renderFirstPreview: boolean; questionPanel?: { canAnswer: boolean; onAnswerIntent: (intent: PreviewAnswerIntent) => void } }) {
  const { t } = useTranslation();
  const [editing, setEditing] = useState(false);
  const [copyStatus, setCopyStatus] = useState<"idle" | "copied" | "failed">("idle");
  const queryClient = useQueryClient();
  const previewContext = useMemo(
    () => hasHtmlPreview(comment.body) ? createPreviewContext({ kind: "comment", commentId: comment.id }) : undefined,
    [comment.body, comment.id, createPreviewContext],
  );
  const mutation = useMutation({ mutationFn: (body: string) => issueApi.updateComment(owner, repo, comment.id, body), onSuccess: async () => { setEditing(false); await queryClient.invalidateQueries({ queryKey: ["issues", owner, repo] }); } });
  const canEdit = canTriage || (canContribute && comment.user.login === currentLogin);
  const copyLink = async () => {
    const permalink = new URL(`${issuePath}#issuecomment-${comment.id}`, window.location.origin).href;
    try {
      await copyText(permalink);
      setCopyStatus("copied");
    } catch {
      setCopyStatus("failed");
    }
  };
  const copyLabel = t(copyStatus === "copied" ? "issues.detail.linkCopied" : copyStatus === "failed" ? "issues.detail.copyLinkFailed" : "issues.detail.copyLink");
  const wasEdited = isLaterTimestamp(comment.updated_at, comment.created_at);
  const questionId = questionPanel ? questionCommentId(comment.body) : null;
  return <article className="timeline-card" id={`issuecomment-${comment.id}`} tabIndex={-1}><header><Avatar login={comment.user.login} displayName={comment.user.name} src={comment.user.avatar_url} /><span><AuthorName user={comment.user} /><small><Trans i18nKey={wasEdited ? "issues.detail.commentedAndEditedRelative" : "issues.detail.commentedRelative"} components={{ createdTime: <PreciseRelativeTime value={comment.created_at} now={now} />, editedTime: <PreciseRelativeTime value={comment.updated_at} now={now} /> }} /></small></span><div className="comment-actions"><button className="quiet-action" type="button" onClick={copyLink} aria-live="polite">{copyStatus === "copied" ? <Check aria-hidden="true" /> : <Link2 aria-hidden="true" />}{copyLabel}</button>{canEdit && !editing ? <button className="quiet-action" type="button" onClick={() => setEditing(true)}><Pencil aria-hidden="true" />{t("issues.detail.edit")}</button> : null}</div></header><div className="timeline-body">{editing ? <CommentEditor initial={comment.body} submitLabel={t("issues.detail.saveComment")} pending={mutation.isPending} error={mutation.error} onCancel={() => setEditing(false)} onSubmit={(body) => mutation.mutate(body)} /> : <MarkdownView source={comment.body} previewContext={hasHtmlPreview(comment.body) ? previewContext : undefined} renderFirstPreview={renderFirstPreview} />}</div>{!editing && questionPanel && questionId ? <QuestionAnswerPanel owner={owner} repo={repo} number={number} questionId={questionId} now={now} canAnswer={questionPanel.canAnswer} onAnswerIntent={questionPanel.onAnswerIntent} /> : null}{!editing ? <ReactionPicker owner={owner} repo={repo} commentId={comment.id} summary={comment.reactions} currentLogin={currentLogin} readOnly={!canContribute} /> : null}</article>;
}

export function IssueDetail({ active }: { active: ActiveRepository }) {
  const { t } = useTranslation();
  const now = useSecondClock();
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
  const [pendingAnswer, setPendingAnswer] = useState<PreviewAnswerIntent | null>(null);
  const [previewActivity] = useState(() => createHtmlPreviewActivity(2));
  const meta = useMeta();
  const previewExecution = Boolean(meta.data?.features.html_preview_execution);
  const answerExecution = Boolean(meta.data?.features.interactive_question_answers && canContribute);
  const onAnswerIntent = useCallback((intent: PreviewAnswerIntent) => setPendingAnswer(intent), []);
  const questionPanel = meta.data?.features.interactive_question_answers && active.authenticated
    ? { canAnswer: answerExecution, onAnswerIntent }
    : undefined;
  const createPreviewContext = useCallback((source: PreviewSource): HtmlPreviewContext | undefined => {
    if (!previewExecution) return undefined;
    return {
      sourceKey: source.kind === "issue" ? `issue:${number}` : `comment:${source.commentId}`,
      activity: previewActivity,
      answersEnabled: answerExecution,
      onAnswerIntent: answerExecution ? onAnswerIntent : undefined,
      previewURL: (id, digest) => issueApi.previewDocumentURL(owner, repo, number, id, digest, source),
    };
  }, [answerExecution, number, onAnswerIntent, owner, previewActivity, previewExecution, repo]);
  const issuePreviewContext = useMemo(() => createPreviewContext({ kind: "issue" }), [createPreviewContext]);
  const issue = useQuery({ queryKey: ["issues", owner, repo, number], queryFn: ({ signal }) => issueApi.getIssue(owner, repo, number, signal), enabled: Number.isInteger(number) && number > 0 });
  const relationships = useQuery({ queryKey: ["issues", owner, repo, number, "relationships"], queryFn: ({ signal }) => issueApi.getRelationships(owner, repo, number, signal), enabled: issue.isSuccess });
  const comments = useQuery({ queryKey: ["issues", owner, repo, number, "comments"], queryFn: ({ signal }) => issueApi.listComments(owner, repo, number, signal), enabled: issue.isSuccess });
  useCommentAnchor({ comments: comments.data, hash: location.hash, owner, repo, number });
  const defaultPreviewSource = useMemo(
    () => selectDefaultPreviewSource(issue.data?.body ?? "", comments.data, location.hash),
    [comments.data, issue.data?.body, location.hash],
  );
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
  return <div className="issue-page issue-detail-page"><header className="detail-header"><div className="detail-title"><Link className="issue-back" to={repositoryIssuePath(active)}>← {owner} / {repo}</Link><h1>{item.title} <span>#{item.number}</span></h1><div className="detail-state-row"><span className={`state-badge ${item.state}`}><CircleDot aria-hidden="true" />{t(item.state === "open" ? "issues.detail.stateOpen" : "issues.detail.stateClosed")}</span><span><Trans i18nKey="issues.detail.openedOnRelative" values={{ actor: displayActor(item.user) }} components={{ time: <PreciseRelativeTime value={item.created_at} now={now} /> }} /></span><span>{t("issues.detail.comments", { count: item.comments })}</span></div></div>{canTriage ? <div className="header-actions"><button className="issue-button" type="button" onClick={() => setEditing((value) => !value)}><Pencil aria-hidden="true" />{t(editing ? "issues.detail.cancelEdit" : "issues.detail.edit")}</button><button className="issue-button" type="button" disabled={updateState.isPending} onClick={() => updateState.mutate(item.state === "open" ? "closed" : "open")}><RotateCcw aria-hidden="true" />{t(item.state === "open" ? "issues.detail.close" : "issues.detail.reopen")}</button></div> : null}</header>
    <MutationProblem error={updateState.error} />
    {pendingAnswer ? <AnswerConfirmation key={`${pendingAnswer.questionId}:${pendingAnswer.optionIds.join(",")}:${pendingAnswer.custom}`} owner={owner} repo={repo} number={number} intent={pendingAnswer} onClose={() => setPendingAnswer(null)} /> : null}
    <div className="detail-grid"><div className="timeline"><article className="timeline-card issue-origin"><header><Avatar login={item.user.login} displayName={item.user.name} src={item.user.avatar_url} tone="coral" /><span><AuthorName user={item.user} /><small>{t("issues.detail.openingNote")}</small></span></header><div className="timeline-body">{editing ? <IssueEditor initial={{ title: item.title, body: item.body, labels: item.labels.map((label) => label.name) }} labels={labels.data ?? []} submitLabel={t("issues.detail.saveIssue")} pending={updateIssue.isPending} error={updateIssue.error} onCancel={() => setEditing(false)} onSubmit={(draft) => updateIssue.mutate(draft)} /> : item.body ? <MarkdownView source={item.body} previewContext={hasHtmlPreview(item.body) ? issuePreviewContext : undefined} renderFirstPreview={defaultPreviewSource === "issue"} /> : <p className="issue-empty-copy">{t("issues.detail.noDescription")}</p>}</div></article>
      {comments.isLoading ? <IssueLoading label={t("issues.detail.loadingConversation")} /> : comments.data?.map((comment) => <EditableComment key={comment.id} comment={comment} owner={owner} repo={repo} number={number} issuePath={repositoryIssuePath(active, item.number)} currentLogin={current.data?.user.login ?? ""} canContribute={canContribute} canTriage={canTriage} now={now} createPreviewContext={createPreviewContext} renderFirstPreview={defaultPreviewSource === `comment:${comment.id}`} questionPanel={questionPanel} />)}
      {canContribute ? <section className="new-comment" aria-labelledby="new-comment-heading"><h2 id="new-comment-heading">{t("issues.detail.continueConversation")}</h2><CommentEditor key={comments.data?.length ?? 0} pending={createComment.isPending} error={createComment.error} onSubmit={(body) => createComment.mutate(body)} /></section> : <section className="new-comment read-only-note" aria-label={t("issues.detail.readOnlyAccess")}><h2>{t("issues.detail.readOnlyConversation")}</h2><p>{t(active.authenticated ? "issues.detail.noCommentPermission" : "issues.detail.signInToComment")}</p>{active.authenticated ? null : <Link className="issue-button" to="/login" state={{ returnTo: repositoryIssuePath(active, item.number) }}>{t("issues.detail.signIn")}</Link>}</section>}</div>
      <aside className="issue-sidebar" aria-label={t("issues.detail.metadata")}><section><span className="issue-kicker purple">{t("issues.detail.labels")}</span><LabelChips labels={item.labels} />{canTriage ? <details><summary>{t("issues.detail.manageLabels")}</summary><LabelSelector labels={labels.data ?? []} selected={item.labels.map((label) => label.name)} disabled={replaceLabels.isPending} onChange={(names) => replaceLabels.mutate(names)} /></details> : null}<MutationProblem error={replaceLabels.error} /></section><section className="issue-relationships" aria-labelledby="issue-relationships-heading"><span className="issue-kicker coral" id="issue-relationships-heading">{t("issues.detail.codeChanges")}</span>{relationships.isLoading ? <p className="relationship-status" role="status">{t("issues.detail.loadingDelivery")}</p> : relationships.error ? <p className="relationship-status relationship-error" role="alert">{t("issues.detail.deliveryUnavailable")}</p> : <CodeChangeList relationships={relationships.data?.relationships ?? []} />}</section><section><span className="issue-kicker">{t("issues.detail.authority")}</span><p><LockKeyhole aria-hidden="true" />{t(`common.permission.${active.repository.effective_permission}`)}</p><small>{t(active.authenticated ? "issues.detail.serverChecks" : "issues.detail.publicRead")}</small></section></aside></div>
  </div>;
}

export function IssueDetailPage() { return <RepositoryGate>{(active) => <IssueDetail active={active} />}</RepositoryGate>; }
