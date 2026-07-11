import { useState, type FormEvent } from "react";
import { MarkdownView } from "../../components/markdown/markdown-view";
import { LabelSelector } from "../../components/labels/label-chips";
import type { Label } from "./types";
import { MutationProblem } from "./repository-context";

export type IssueDraft = { title: string; body: string; labels: string[] };

export function IssueEditor({ initial, labels, submitLabel, pending, error, onCancel, onSubmit }: { initial: IssueDraft; labels: Label[]; submitLabel: string; pending: boolean; error?: unknown; onCancel?: () => void; onSubmit: (draft: IssueDraft) => void }) {
  const [title, setTitle] = useState(initial.title);
  const [body, setBody] = useState(initial.body);
  const [selected, setSelected] = useState(initial.labels);
  const [preview, setPreview] = useState(false);
  const submit = (event: FormEvent) => { event.preventDefault(); if (title.trim()) onSubmit({ title, body, labels: selected }); };
  return <form className="issue-editor" onSubmit={submit}>
    <label className="issue-field"><span>Title</span><input value={title} maxLength={256} required onChange={(event) => setTitle(event.target.value)} /></label>
    <div className="editor-tabs" role="tablist" aria-label="Issue description view"><button type="button" role="tab" aria-selected={!preview} onClick={() => setPreview(false)}>Write</button><button type="button" role="tab" aria-selected={preview} onClick={() => setPreview(true)}>Preview</button></div>
    {preview ? <div className="editor-preview"><MarkdownView source={body} /></div> : <label className="issue-field"><span className="sr-only">Description</span><textarea value={body} rows={14} placeholder="Describe the outcome, context, and acceptance criteria…" onChange={(event) => setBody(event.target.value)} /></label>}
    <p className="field-note">GitHub-flavored Markdown is supported. Workflow markers stay in the raw text and are hidden only in rendered views.</p>
    <LabelSelector labels={labels} selected={selected} disabled={pending} onChange={setSelected} />
    <MutationProblem error={error} />
    <div className="editor-actions">{onCancel ? <button className="issue-button" type="button" onClick={onCancel}>Cancel</button> : null}<button className="issue-button primary" disabled={pending || !title.trim()} type="submit">{pending ? "Saving…" : submitLabel}</button></div>
  </form>;
}

export function CommentEditor({ initial = "", submitLabel = "Comment", pending, error, onCancel, onSubmit }: { initial?: string; submitLabel?: string; pending: boolean; error?: unknown; onCancel?: () => void; onSubmit: (body: string) => void }) {
  const [body, setBody] = useState(initial);
  const [preview, setPreview] = useState(false);
  return <form className="comment-editor" onSubmit={(event) => { event.preventDefault(); if (body.trim()) onSubmit(body); }}>
    <div className="editor-tabs" role="tablist" aria-label="Comment view"><button type="button" role="tab" aria-selected={!preview} onClick={() => setPreview(false)}>Write</button><button type="button" role="tab" aria-selected={preview} onClick={() => setPreview(true)}>Preview</button></div>
    {preview ? <div className="editor-preview"><MarkdownView source={body} /></div> : <label className="issue-field"><span className="sr-only">Comment</span><textarea aria-label="Comment" value={body} rows={7} placeholder="Add context or record a decision…" onChange={(event) => setBody(event.target.value)} /></label>}
    <MutationProblem error={error} />
    <div className="editor-actions">{onCancel ? <button className="issue-button" type="button" onClick={onCancel}>Cancel</button> : null}<button className="issue-button primary" disabled={pending || !body.trim()} type="submit">{pending ? "Saving…" : submitLabel}</button></div>
  </form>;
}
