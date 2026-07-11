import { useEffect, useMemo, useState } from "react";
import { useMutation, useQuery, useQueryClient } from "@tanstack/react-query";
import { useForm } from "react-hook-form";
import {
  Activity, Cable, CheckCircle2, Clock3, ExternalLink, GitBranch, PauseCircle,
  PlayCircle, Plus, RadioTower, RefreshCw, RotateCw, Send, ShieldCheck, Trash2,
} from "lucide-react";
import { Link, useParams } from "react-router-dom";
import { EmptyState, ErrorNotice, Field, Loading, PageHeader, Panel, SecretDialog, StatusBadge, TextInput } from "../app/components";
import { useInspector } from "../app/problem-inspector";
import { queryKeys, useMeta } from "../auth/session";
import { api } from "../lib/api/resources";
import type { SourceBinding, WebhookDelivery, WebhookRetry, WebhookSecret, WebhookSubscription } from "../lib/api/types";
import "./integrations.css";

type IntegrationKind = "source" | "webhooks";
type SourceBindingDraft = Pick<SourceBinding, "provider_key" | "external_repository_id" | "clone_url" | "web_url" | "default_branch">;
type WebhookDraft = { url: string; event_types: string[]; max_attempts: number; initial_backoff: string; max_backoff: string };

const eventOptions = [
  ["issue_comment.created", "New comment"],
  ["issue_comment.edited", "Edited comment"],
  ["issue.created", "New issue"],
  ["issue.edited", "Edited issue"],
  ["issue.closed", "Closed issue"],
] as const;

export function IntegrationsPage({ kind }: { kind: IntegrationKind }) {
  const { orgId = "", repoId = "" } = useParams();
  const meta = useMeta();
  const capability = kind === "source" ? "source_bindings" : "webhooks";
  if (meta.isLoading) return <Loading label="Opening integration workspace" />;
  if (meta.error) return <ErrorNotice error={meta.error} />;
  if (!meta.data?.features[capability]) {
    return <div className="page"><IntegrationHeader kind={kind} orgId={orgId} repoId={repoId} /><Panel><EmptyState title="Capability unavailable" description="This server did not mount the required native integration capability." action={<StatusBadge tone="coral">not mounted</StatusBadge>} /></Panel></div>;
  }
  return <div className="page integrations-page"><IntegrationHeader kind={kind} orgId={orgId} repoId={repoId} />{kind === "source" ? <SourceWorkspace orgId={orgId} repoId={repoId} /> : <WebhookWorkspace orgId={orgId} repoId={repoId} />}</div>;
}

function IntegrationHeader({ kind, orgId, repoId }: { kind: IntegrationKind; orgId: string; repoId: string }) {
  return <PageHeader eyebrow="Repository / integrations" title={kind === "source" ? "Source connection" : "Delivery control room"} description={kind === "source" ? "Bind repository identity without storing source-host credentials." : "Route repository events to trusted runners, inspect every attempt, and replay failures."} actions={<nav className="integration-switcher" aria-label="Integration sections"><Link className={kind === "source" ? "active" : ""} to={`/orgs/${orgId}/repos/${repoId}/integrations/source`}><Cable size={16} />Source</Link><Link className={kind === "webhooks" ? "active" : ""} to={`/orgs/${orgId}/repos/${repoId}/integrations/webhooks`}><RadioTower size={16} />Webhooks</Link></nav>} />;
}

function SourceWorkspace({ orgId, repoId }: { orgId: string; repoId: string }) {
  const inspector = useInspector();
  const client = useQueryClient();
  const binding = useQuery({ queryKey: queryKeys.sourceBinding(orgId, repoId), queryFn: ({ signal }) => api.activeSourceBinding(orgId, repoId, signal) });
  const [confirmDeactivate, setConfirmDeactivate] = useState(false);
  const { register, handleSubmit, reset, formState: { errors } } = useForm<SourceBindingDraft>({ defaultValues: emptyBindingDraft });
  useEffect(() => reset(binding.data ? bindingDraft(binding.data) : emptyBindingDraft), [binding.data, reset]);
  const refresh = () => client.invalidateQueries({ queryKey: queryKeys.sourceBinding(orgId, repoId) });
  const publish = useMutation({
    mutationFn: (draft: SourceBindingDraft) => api.createSourceBinding(orgId, repoId, draft),
    onSuccess: (created) => { inspector.note(`Source binding v${created.version} is active.`); setConfirmDeactivate(false); void refresh(); },
    onError: (error, draft) => inspector.report(error, draft),
  });
  const deactivate = useMutation({
    mutationFn: () => api.deactivateSourceBinding(orgId, repoId),
    onSuccess: () => { inspector.note("Source binding deactivated. Historical versions remain auditable."); setConfirmDeactivate(false); void refresh(); },
    onError: inspector.report,
  });
  if (binding.isLoading) return <Loading label="Reading active source binding" />;
  return <>
    {binding.error ? <ErrorNotice error={binding.error} /> : null}
    <section className="integration-hero" aria-label="Source binding status">
      <div className="integration-hero-mark"><GitBranch aria-hidden="true" /></div>
      <div><span className="eyebrow">Credential-free identity</span><h2>{binding.data ? binding.data.external_repository_id : "No source connected"}</h2><p>{binding.data ? `${binding.data.provider_key} · ${binding.data.default_branch} · binding version ${binding.data.version}` : "Connect a canonical source identity so runner clones and evidence links resolve consistently."}</p></div>
      <StatusBadge tone={binding.data ? "teal" : "neutral"}>{binding.data ? "active" : "unbound"}</StatusBadge>
    </section>
    {binding.data ? <Panel className="binding-summary"><div className="binding-route"><span className="route-node"><Cable size={17} />Local repository</span><span className="route-line" aria-hidden="true" /><a className="route-node external" href={binding.data.web_url} target="_blank" rel="noopener noreferrer"><ExternalLink size={17} />{binding.data.provider_key}</a></div><dl className="integration-facts"><div><dt>External repository</dt><dd>{binding.data.external_repository_id}</dd></div><div><dt>Clone URL</dt><dd className="mono break-word">{binding.data.clone_url}</dd></div><div><dt>Default branch</dt><dd>{binding.data.default_branch}</dd></div><div><dt>Updated</dt><dd>{formatDate(binding.data.updated_at)}</dd></div></dl></Panel> : null}
    <Panel title={binding.data ? "Publish a new binding version" : "Connect source identity"} description="A new version atomically replaces the active binding. URLs must not contain embedded credentials.">
      <form className="integration-form" onSubmit={handleSubmit((draft) => publish.mutate(draft))}>
        <Field label="Provider key" hint="A neutral adapter key such as github or gitlab." error={errors.provider_key?.message}><TextInput autoComplete="off" {...register("provider_key", { required: "Provider key is required" })} /></Field>
        <Field label="External repository ID" hint="Stable provider identity, for example owner/repository." error={errors.external_repository_id?.message}><TextInput autoComplete="off" {...register("external_repository_id", { required: "Repository identity is required" })} /></Field>
        <Field label="Clone URL" hint="HTTPS clone target; credentials and fragments are rejected." error={errors.clone_url?.message}><TextInput type="url" autoComplete="off" {...register("clone_url", { required: "Clone URL is required" })} /></Field>
        <Field label="Web URL" hint="Human-facing canonical repository page." error={errors.web_url?.message}><TextInput type="url" autoComplete="off" {...register("web_url", { required: "Web URL is required" })} /></Field>
        <Field label="Default branch" error={errors.default_branch?.message}><TextInput autoComplete="off" {...register("default_branch", { required: "Default branch is required" })} /></Field>
        <div className="integration-form-actions"><button className="button primary" type="submit" disabled={publish.isPending}><GitBranch size={16} />{publish.isPending ? "Publishing…" : binding.data ? "Publish new version" : "Connect source"}</button>{binding.data ? <button className="button danger" type="button" disabled={deactivate.isPending} onClick={() => confirmDeactivate ? deactivate.mutate(undefined) : setConfirmDeactivate(true)}><Trash2 size={16} />{confirmDeactivate ? "Confirm deactivation" : "Deactivate"}</button> : null}{confirmDeactivate ? <button className="button secondary" type="button" onClick={() => setConfirmDeactivate(false)}>Keep active</button> : null}</div>
      </form>
    </Panel>
    <div className="trust-note"><ShieldCheck size={20} /><div><strong>No provider credential crosses this boundary.</strong><p>The binding stores repository identity and public endpoints only. Runner credentials are delegated per job.</p></div></div>
  </>;
}

function WebhookWorkspace({ orgId, repoId }: { orgId: string; repoId: string }) {
  const inspector = useInspector();
  const client = useQueryClient();
  const [creating, setCreating] = useState(false);
  const [editing, setEditing] = useState<string>();
  const [confirmRevoke, setConfirmRevoke] = useState<string>();
  const [secret, setSecret] = useState<{ value: string; title: string }>();
  const subscriptions = useQuery({ queryKey: queryKeys.webhooks(orgId, repoId), queryFn: ({ signal }) => api.webhookSubscriptions(orgId, repoId, signal) });
  const refresh = () => client.invalidateQueries({ queryKey: queryKeys.webhooks(orgId, repoId) });
  const pause = useMutation({
    mutationFn: ({ item, active }: { item: WebhookSubscription; active: boolean }) => api.updateWebhookSubscription(orgId, item.id, webhookUpdate(item, active)),
    onSuccess: (_, variables) => { inspector.note(`Webhook ${variables.active ? "resumed" : "paused"}.`); void refresh(); },
    onError: (error, variables) => inspector.report(error, variables),
  });
  const rotate = useMutation({
    mutationFn: (id: string) => api.rotateWebhookSecret(orgId, id),
    onSuccess: (result) => { setSecret({ value: result.secret, title: `Webhook secret v${result.secret_version}` }); inspector.note("Webhook secret rotated with bounded overlap."); void refresh(); },
    onError: inspector.report,
  });
  const revoke = useMutation({
    mutationFn: (id: string) => api.revokeWebhookSubscription(orgId, id),
    onSuccess: () => { setConfirmRevoke(undefined); setEditing(undefined); inspector.note("Webhook revoked."); void refresh(); },
    onError: inspector.report,
  });
  const items = subscriptions.data?.subscriptions ?? [];
  return <>
    <section className="integration-hero webhook-hero" aria-label="Webhook overview"><div className="integration-hero-mark"><Activity aria-hidden="true" /></div><div><span className="eyebrow">Repository event transport</span><h2>{items.filter((item) => item.active).length} active route{items.filter((item) => item.active).length === 1 ? "" : "s"}</h2><p>Signed, ordered delivery with visible retry state and operator-controlled replay.</p></div><button className="button primary" type="button" onClick={() => setCreating((value) => !value)}><Plus size={16} />{creating ? "Close form" : "New webhook"}</button></section>
    {creating ? <WebhookEditor orgId={orgId} repoId={repoId} onSaved={(created) => { if (!("secret" in created)) return; setCreating(false); setSecret({ value: created.secret, title: `Webhook secret v${created.secret_version}` }); void refresh(); }} /> : null}
    <Panel title="Webhook routes" description="Each route is repository-scoped; pause keeps its configuration and audit history.">
      {subscriptions.isLoading ? <Loading label="Loading webhook routes" /> : null}
      {subscriptions.error ? <ErrorNotice error={subscriptions.error} /> : null}
      {!subscriptions.isLoading && items.length === 0 ? <EmptyState title="No webhook routes yet" description="Create a route to connect this repository to runner serve or another approved receiver." action={<button className="button primary" type="button" onClick={() => setCreating(true)}><Plus size={16} />Create webhook</button>} /> : null}
      <div className="webhook-list">{items.map((item) => <article className={`webhook-card ${item.active ? "" : "paused"}`} key={item.id}><header><span className={`pulse ${item.active ? "active" : ""}`} aria-hidden="true" /><div><strong className="break-word">{item.url}</strong><span>Repository route · v{item.representation_version} · updated {formatDate(item.updated_at)}</span></div><StatusBadge tone={item.active ? "teal" : "neutral"}>{item.active ? "active" : "paused"}</StatusBadge></header><div className="event-strip">{item.event_types.map((event) => <span key={event}>{event}</span>)}</div><dl className="retry-line"><div><dt>Attempts</dt><dd>{item.retry.max_attempts}</dd></div><div><dt>First retry</dt><dd>{item.retry.initial_backoff}</dd></div><div><dt>Retry ceiling</dt><dd>{item.retry.max_backoff}</dd></div></dl><div className="row-actions"><button className="button secondary small" type="button" onClick={() => setEditing(editing === item.id ? undefined : item.id)}>Configure</button><button className="button secondary small" type="button" disabled={pause.isPending} onClick={() => pause.mutate({ item, active: !item.active })}>{item.active ? <PauseCircle size={15} /> : <PlayCircle size={15} />}{item.active ? "Pause" : "Resume"}</button><button className="button secondary small" type="button" disabled={rotate.isPending} onClick={() => rotate.mutate(item.id)}><RotateCw size={15} />Rotate secret</button><button className="button danger small" type="button" disabled={revoke.isPending} onClick={() => confirmRevoke === item.id ? revoke.mutate(item.id) : setConfirmRevoke(item.id)}><Trash2 size={15} />{confirmRevoke === item.id ? "Confirm revoke" : "Revoke"}</button>{confirmRevoke === item.id ? <button className="button secondary small" type="button" onClick={() => setConfirmRevoke(undefined)}>Cancel</button> : null}</div>{editing === item.id ? <WebhookEditor orgId={orgId} repoId={repoId} subscription={item} onSaved={() => { setEditing(undefined); void refresh(); }} /> : null}</article>)}</div>
    </Panel>
    <DeliveryConsole orgId={orgId} repoId={repoId} />
    {secret ? <SecretDialog secret={secret.value} title={secret.title} onClose={() => setSecret(undefined)} /> : null}
  </>;
}

function WebhookEditor({ orgId, repoId, subscription, onSaved }: { orgId: string; repoId: string; subscription?: WebhookSubscription; onSaved: (result: WebhookSubscription | WebhookSecret) => void }) {
  const inspector = useInspector();
  const defaults = useMemo(() => subscription ? webhookDraft(subscription) : emptyWebhookDraft, [subscription]);
  const { register, handleSubmit, formState: { errors } } = useForm<WebhookDraft>({ defaultValues: defaults });
  const save = useMutation({
    mutationFn: (draft: WebhookDraft) => {
      if (!draft.event_types?.length) throw new Error("Select at least one event type");
      const retry = retryFromDraft(draft);
      return subscription
        ? api.updateWebhookSubscription(orgId, subscription.id, { url: draft.url, event_types: draft.event_types, retry, active: subscription.active, expected_version: subscription.representation_version })
        : api.createWebhookSubscription(orgId, { repository_id: repoId, url: draft.url, event_types: draft.event_types, retry });
    },
    onSuccess: (result) => { inspector.note(subscription ? "Webhook configuration saved." : "Webhook created. Save the secret before closing the dialog."); onSaved(result); },
    onError: (error, draft) => inspector.report(error, draft),
  });
  return <form className="webhook-editor" onSubmit={handleSubmit((draft) => save.mutate(draft))}><div className="integration-form compact"><Field label="Receiver URL" hint="HTTPS is required in production; redirects are never followed." error={errors.url?.message}><TextInput type="url" placeholder="https://runner.example.test/api/v1/runner/webhooks" {...register("url", { required: "Receiver URL is required" })} /></Field><Field label="Maximum attempts" error={errors.max_attempts?.message}><TextInput type="number" min={1} max={100} {...register("max_attempts", { valueAsNumber: true, required: "Retry count is required", min: { value: 1, message: "Use at least one attempt" }, max: { value: 100, message: "Maximum is 100" } })} /></Field><Field label="Initial backoff"><TextInput placeholder="1s" {...register("initial_backoff", { required: true })} /></Field><Field label="Maximum backoff"><TextInput placeholder="5m" {...register("max_backoff", { required: true })} /></Field></div><fieldset className="event-picker"><legend>Events</legend>{eventOptions.map(([value, label]) => <label key={value}><input type="checkbox" value={value} {...register("event_types")} /><span><strong>{label}</strong><small>{value}</small></span></label>)}</fieldset><button className="button primary" type="submit" disabled={save.isPending}><Send size={16} />{save.isPending ? "Saving…" : subscription ? "Save route" : "Create route"}</button></form>;
}

function DeliveryConsole({ orgId, repoId }: { orgId: string; repoId: string }) {
  const inspector = useInspector();
  const client = useQueryClient();
  const [selected, setSelected] = useState<string>();
  const deliveries = useQuery({ queryKey: queryKeys.deliveries(orgId, repoId), queryFn: ({ signal }) => api.webhookDeliveries(orgId, repoId, signal) });
  const detail = useQuery({ queryKey: queryKeys.delivery(orgId, repoId, selected ?? ""), queryFn: ({ signal }) => api.webhookDelivery(orgId, repoId, selected ?? "", signal), enabled: Boolean(selected) });
  const replay = useMutation({ mutationFn: (id: string) => api.redeliverWebhookDelivery(orgId, repoId, id), onSuccess: (item) => { inspector.note(`Delivery ${shortID(item.id)} queued for replay.`); void client.invalidateQueries({ queryKey: queryKeys.deliveries(orgId, repoId) }); void client.invalidateQueries({ queryKey: queryKeys.delivery(orgId, repoId, item.id) }); }, onError: inspector.report });
  const items = deliveries.data?.deliveries ?? [];
  const succeeded = items.filter((item) => item.state === "succeeded").length;
  const dead = items.filter((item) => item.state === "dead").length;
  return <Panel className="delivery-panel" title="Delivery ledger" description="Immutable event identity, attempt history, and manual replay stay together."><div className="delivery-toolbar"><div className="delivery-metrics"><span aria-label={`${items.length} recorded`}><strong>{items.length}</strong> recorded</span><span aria-label={`${succeeded} delivered`}><strong>{succeeded}</strong> delivered</span><span aria-label={`${dead} dead ${dead === 1 ? "letter" : "letters"}`}><strong>{dead}</strong> dead {dead === 1 ? "letter" : "letters"}</span></div><button className="icon-button" type="button" aria-label="Refresh deliveries" onClick={() => void deliveries.refetch()}><RefreshCw size={17} /></button></div>{deliveries.isLoading ? <Loading label="Loading delivery ledger" /> : null}{deliveries.error ? <ErrorNotice error={deliveries.error} /> : null}{!deliveries.isLoading && items.length === 0 ? <EmptyState title="No deliveries recorded" description="Delivery records appear after a subscribed repository event enters the outbox." /> : null}<div className="delivery-console"><div className="delivery-list">{items.map((item) => <button className={`delivery-row ${selected === item.id ? "selected" : ""}`} type="button" key={item.id} onClick={() => setSelected(item.id)}><span className={`delivery-state ${item.state}`} aria-hidden="true" /><span><strong>{item.event_type}</strong><small>#{item.repository_sequence} · {shortID(item.id)}</small></span><StatusBadge tone={deliveryTone(item)}>{item.state}</StatusBadge><time>{formatDate(item.updated_at)}</time></button>)}</div>{selected ? <aside className="delivery-detail" aria-label="Delivery detail">{detail.isLoading ? <Loading label="Reading attempts" /> : null}{detail.error ? <ErrorNotice error={detail.error} /> : null}{detail.data ? <><header><div><span className="eyebrow">Delivery {shortID(detail.data.delivery.id)}</span><h3>{detail.data.delivery.event_type}</h3></div><StatusBadge tone={deliveryTone(detail.data.delivery)}>{detail.data.delivery.state}</StatusBadge></header><dl className="integration-facts compact"><div><dt>Event ID</dt><dd className="mono break-word">{detail.data.delivery.event_id}</dd></div><div><dt>Secret version</dt><dd>v{detail.data.delivery.secret_version}</dd></div><div><dt>Next attempt</dt><dd>{formatDate(detail.data.delivery.next_attempt_at)}</dd></div>{detail.data.delivery.last_error ? <div><dt>Last error</dt><dd>{detail.data.delivery.last_error}</dd></div> : null}</dl><div className="attempts"><h4>Attempts</h4>{detail.data.attempts.length ? detail.data.attempts.map((attempt) => <article key={attempt.id}><span className="attempt-number">{attempt.attempt_number}</span><div><strong>{attempt.response_status ? `HTTP ${attempt.response_status}` : attempt.error ?? "Transport attempt"}</strong><small>{formatDate(attempt.started_at)}{attempt.completed_at ? ` · completed ${formatDate(attempt.completed_at)}` : " · in progress"}</small></div>{attempt.response_status && attempt.response_status >= 200 && attempt.response_status < 300 ? <CheckCircle2 size={17} className="teal-text" /> : <Clock3 size={17} />}</article>) : <p>No attempt has started yet.</p>}</div><button className="button secondary" type="button" disabled={replay.isPending} onClick={() => replay.mutate(detail.data.delivery.id)}><RotateCw size={16} />{replay.isPending ? "Queuing…" : "Redeliver event"}</button></> : null}</aside> : null}</div></Panel>;
}

const emptyBindingDraft: SourceBindingDraft = { provider_key: "github", external_repository_id: "", clone_url: "", web_url: "", default_branch: "main" };
const emptyWebhookDraft: WebhookDraft = { url: "", event_types: ["issue_comment.created", "issue_comment.edited"], max_attempts: 8, initial_backoff: "1s", max_backoff: "5m" };
function bindingDraft(binding: SourceBinding): SourceBindingDraft { return { provider_key: binding.provider_key, external_repository_id: binding.external_repository_id, clone_url: binding.clone_url, web_url: binding.web_url, default_branch: binding.default_branch }; }
function webhookDraft(item: WebhookSubscription): WebhookDraft { return { url: item.url, event_types: item.event_types, max_attempts: item.retry.max_attempts, initial_backoff: item.retry.initial_backoff, max_backoff: item.retry.max_backoff }; }
function retryFromDraft(draft: WebhookDraft): WebhookRetry { return { max_attempts: draft.max_attempts, initial_backoff: draft.initial_backoff, max_backoff: draft.max_backoff }; }
function webhookUpdate(item: WebhookSubscription, active: boolean) { return { url: item.url, event_types: item.event_types, retry: item.retry, active, expected_version: item.representation_version }; }
function shortID(id: string) { return id.slice(0, 8); }
function formatDate(value: string) { const date = new Date(value); return Number.isNaN(date.getTime()) ? value : new Intl.DateTimeFormat(undefined, { dateStyle: "medium", timeStyle: "short" }).format(date); }
function deliveryTone(item: WebhookDelivery): "neutral" | "teal" | "purple" | "coral" { if (item.state === "succeeded") return "teal"; if (item.state === "failed" || item.state === "dead") return "coral"; if (item.state === "pending" || item.state === "delivering") return "purple"; return "neutral"; }
