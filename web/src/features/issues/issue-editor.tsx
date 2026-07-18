import { useState, type FormEvent } from "react";
import { MarkdownView } from "../../components/markdown/markdown-view";
import { LabelSelector } from "../../components/labels/label-chips";
import { useTranslation } from "react-i18next";
import type { Label } from "./types";
import { MutationProblem } from "./repository-context";
import { MentionTextarea } from "./mention-autocomplete";

export type IssueDraft = { title: string; body: string; labels: string[] };

export function IssueEditor({ initial, labels, submitLabel, pending, error, onCancel, onSubmit }: { initial: IssueDraft; labels: Label[]; submitLabel: string; pending: boolean; error?: unknown; onCancel?: () => void; onSubmit: (draft: IssueDraft) => void }) {
  const { t } = useTranslation();
  const [title, setTitle] = useState(initial.title);
  const [body, setBody] = useState(initial.body);
  const [selected, setSelected] = useState(initial.labels);
  const [preview, setPreview] = useState(false);
  const submit = (event: FormEvent) => { event.preventDefault(); if (title.trim()) onSubmit({ title, body, labels: selected }); };
  return <form className="issue-editor" onSubmit={submit}>
    <label className="issue-field"><span>{t("issues.editor.title")}</span><input value={title} maxLength={256} required onChange={(event) => setTitle(event.target.value)} /></label>
    <div className="editor-tabs" role="tablist" aria-label={t("issues.editor.issueView")}><button type="button" role="tab" aria-selected={!preview} onClick={() => setPreview(false)}>{t("issues.editor.write")}</button><button type="button" role="tab" aria-selected={preview} onClick={() => setPreview(true)}>{t("issues.editor.preview")}</button></div>
    {preview ? <div className="editor-preview"><MarkdownView source={body} /></div> : <label className="issue-field"><span className="sr-only">{t("issues.editor.description")}</span><textarea value={body} rows={14} placeholder={t("issues.editor.descriptionPlaceholder")} onChange={(event) => setBody(event.target.value)} /></label>}
    <p className="field-note">{t("issues.editor.markdownHelp")}</p>
    <LabelSelector labels={labels} selected={selected} disabled={pending} onChange={setSelected} />
    <MutationProblem error={error} />
    <div className="editor-actions">{onCancel ? <button className="issue-button" type="button" onClick={onCancel}>{t("issues.editor.cancel")}</button> : null}<button className="issue-button primary" disabled={pending || !title.trim()} type="submit">{pending ? t("issues.editor.saving") : submitLabel}</button></div>
  </form>;
}

export function CommentEditor({ initial = "", submitLabel, pending, error, onCancel, onSubmit }: { initial?: string; submitLabel?: string; pending: boolean; error?: unknown; onCancel?: () => void; onSubmit: (body: string) => void }) {
  const { t } = useTranslation();
  const [body, setBody] = useState(initial);
  const [preview, setPreview] = useState(false);
  return <form className="comment-editor" onSubmit={(event) => { event.preventDefault(); if (body.trim()) onSubmit(body); }}>
    <div className="editor-tabs" role="tablist" aria-label={t("issues.editor.commentView")}><button type="button" role="tab" aria-selected={!preview} onClick={() => setPreview(false)}>{t("issues.editor.write")}</button><button type="button" role="tab" aria-selected={preview} onClick={() => setPreview(true)}>{t("issues.editor.preview")}</button></div>
    {preview ? <div className="editor-preview"><MarkdownView source={body} /></div> : <label className="issue-field"><span className="sr-only">{t("issues.editor.comment")}</span><MentionTextarea label={t("issues.editor.comment")} value={body} rows={7} placeholder={t("issues.editor.commentPlaceholder")} onChange={setBody} /></label>}
    <MutationProblem error={error} />
    <div className="editor-actions">{onCancel ? <button className="issue-button" type="button" onClick={onCancel}>{t("issues.editor.cancel")}</button> : null}<button className="issue-button primary" disabled={pending || !body.trim()} type="submit">{pending ? t("issues.editor.saving") : submitLabel ?? t("issues.editor.comment")}</button></div>
  </form>;
}
