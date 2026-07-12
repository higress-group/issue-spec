import { useEffect, useMemo, useState } from "react";
import { useMutation, useQuery, useQueryClient } from "@tanstack/react-query";
import { useForm, useWatch } from "react-hook-form";
import {
  Activity, Bell, Bot, Cable, CheckCircle2, Clock3, ExternalLink, Filter, GitBranch,
  LockKeyhole, PauseCircle, PlayCircle, Plus, RefreshCw, RotateCw, Send, ShieldCheck, Trash2,
} from "lucide-react";
import { EmptyState, ErrorNotice, Field, Loading, Panel, SecretDialog, SelectInput, StatusBadge, TextInput } from "../app/components";
import { useInspector } from "../app/problem-inspector";
import { queryKeys, useMeta } from "../auth/session";
import { api } from "../lib/api/resources";
import type { AdminRepository, SourceBinding, WebhookContentPolicy, WebhookDelivery, WebhookDeliveryFormat, WebhookRetry, WebhookSecret, WebhookSigningMode, WebhookSubscription } from "../lib/api/types";
import { RepositoryHeader, useRepositoryContext } from "./repository-header";
import "./integrations.css";

type IntegrationKind = "source" | "webhooks";
type SourceBindingDraft = Pick<SourceBinding, "provider_key" | "external_repository_id" | "clone_url" | "web_url" | "default_branch">;
type WebhookDraft = {
  delivery_format: WebhookDeliveryFormat; url: string; event_types: string[]; signing_mode: WebhookSigningMode;
  issue_actions: WebhookContentPolicy["issue_actions"]; comment_actions: WebhookContentPolicy["comment_actions"];
  issue_kinds: WebhookContentPolicy["issue_kinds"]; comment_classes: WebhookContentPolicy["comment_classes"];
  actor_classes: WebhookContentPolicy["actor_classes"]; clear_destination_query: boolean;
  max_attempts: number; initial_backoff: string; max_backoff: string;
};

const eventOptions = [
  ["issue_comment.created", "New comment"],
  ["issue_comment.edited", "Edited comment"],
  ["issue.created", "New issue"],
  ["issue.edited", "Edited issue"],
  ["issue.closed", "Closed issue"],
	["issue.reopened", "Reopened issue"],
] as const;

const issueActionOptions = [["opened", "Opened"], ["edited", "Edited"], ["closed", "Closed"], ["reopened", "Reopened"]] as const;
const commentActionOptions = [["created", "Created"], ["edited", "Edited"]] as const;
const issueKindOptions = [["ordinary", "Ordinary"], ["proposal", "Proposal"], ["design", "Design"], ["implement", "Implement"]] as const;
const commentClassOptions = [["human-untyped", "Human comment"], ["typed", "Typed workflow"]] as const;
const actorClassOptions = [["human", "Authenticated human"], ["automation", "PAT, delegated or service automation"]] as const;

export function IntegrationsPage({ kind }: { kind: IntegrationKind }) {
  const { orgId, repoId, repository } = useRepositoryContext();
  const meta = useMeta();
  const access = useQuery({ queryKey: queryKeys.repoContext(orgId), queryFn: ({ signal }) => api.repositoriesContext(orgId, signal) });
  const capability = kind === "source" ? "source_bindings" : "webhooks";
  if (meta.isLoading || repository.isLoading || access.isLoading) return <Loading label="Opening integration workspace" />;
  if (meta.error) return <ErrorNotice error={meta.error} />;
  if (repository.error) return <ErrorNotice error={repository.error} />;
  if (access.error) return <ErrorNotice error={access.error} />;
  if (!repository.data) return null;
  if (!meta.data?.features[capability]) {
    return <div className="page"><IntegrationHeader kind={kind} repository={repository.data} /><Panel><EmptyState title="Capability unavailable" description="This server did not mount the required native integration capability." action={<StatusBadge tone="coral">not mounted</StatusBadge>} /></Panel></div>;
  }
  const repositoryAccess = access.data?.repositories.find((item) => item.repository.id === repoId);
  const canManage = repositoryAccess?.allowed_actions.includes("integrations.manage") ?? false;
  if (!canManage) return <div className="page integrations-page"><IntegrationHeader kind={kind} repository={repository.data} /><Panel><EmptyState title="Integration management required" description="Webhook destinations, filters, secrets and delivery history are visible only to repository integration managers." action={<StatusBadge tone="coral">restricted</StatusBadge>} /></Panel></div>;
  return <div className="page integrations-page"><IntegrationHeader kind={kind} repository={repository.data} />{kind === "source" ? <SourceWorkspace orgId={orgId} repoId={repoId} /> : <WebhookWorkspace orgId={orgId} repoId={repoId} />}</div>;
}

function IntegrationHeader({ kind, repository }: { kind: IntegrationKind; repository: AdminRepository }) {
  return <RepositoryHeader repository={repository} section={kind} title={kind === "source" ? "Source connection" : "Delivery control room"} description={kind === "source" ? "Bind repository identity without storing source-host credentials." : "Route repository events to trusted runners, inspect every attempt, and replay failures."} />;
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
  const [showSuppressions, setShowSuppressions] = useState<string>();
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
    <section className="integration-hero webhook-hero" aria-label="Webhook overview"><div className="integration-hero-mark"><Activity aria-hidden="true" /></div><div><span className="eyebrow">Repository event transport</span><h2>{items.filter((item) => item.active).length} active route{items.filter((item) => item.active).length === 1 ? "" : "s"}</h2><p>Runner intake and GitHub-compatible notifications share one reliable ledger without sharing authentication semantics.</p></div><button className="button primary" type="button" onClick={() => setCreating((value) => !value)}><Plus size={16} />{creating ? "Close form" : "New webhook"}</button></section>
    {creating ? <WebhookEditor orgId={orgId} repoId={repoId} onSaved={(created) => { setCreating(false); if ("secret" in created && created.signing_mode !== "none") setSecret({ value: created.secret, title: `Webhook secret v${created.secret_version}` }); void refresh(); }} /> : null}
    <Panel title="Webhook routes" description="Pause is resumable. Revoke is terminal and preserves configuration and delivery history for audit only.">
      {subscriptions.isLoading ? <Loading label="Loading webhook routes" /> : null}
      {subscriptions.error ? <ErrorNotice error={subscriptions.error} /> : null}
      {!subscriptions.isLoading && items.length === 0 ? <EmptyState title="No webhook routes yet" description="Create a route to connect this repository to runner serve or another approved receiver." action={<button className="button primary" type="button" onClick={() => setCreating(true)}><Plus size={16} />Create webhook</button>} /> : null}
      <div className="webhook-list">{items.map((item) => {
        const revoked = Boolean(item.revoked_at);
        const lifecycle = revoked ? "revoked" : item.active ? "active" : "paused";
        return <article className={`webhook-card ${lifecycle}`} key={item.id}>
          <header><span className={`pulse ${lifecycle}`} aria-hidden="true" /><div><strong className="break-word">{item.url}</strong><span>{item.delivery_format === "github.v3" ? "GitHub notification" : "Runner intake"} · v{item.representation_version} · updated {formatDate(item.updated_at)}</span></div><StatusBadge tone={revoked ? "coral" : item.active ? "teal" : "neutral"}>{lifecycle}</StatusBadge></header>
          <div className="route-contract"><StatusBadge tone={item.delivery_format === "github.v3" ? "purple" : "teal"}>{item.delivery_format}</StatusBadge><span>{signingLabel(item.signing_mode)}</span>{item.has_destination_query ? <span className="credential-badge"><LockKeyhole size={13} />Encrypted destination credential</span> : null}</div>
          <div className="event-strip">{item.delivery_format === "github.v3" ? policySummary(item.content_policy).map((filter) => <span key={filter}>{filter}</span>) : item.event_types.map((event) => <span key={event}>{event}</span>)}</div>
          <dl className="retry-line"><div><dt>Attempts</dt><dd>{item.retry.max_attempts}</dd></div><div><dt>First retry</dt><dd>{item.retry.initial_backoff}</dd></div><div><dt>Retry ceiling</dt><dd>{item.retry.max_backoff}</dd></div></dl>
          {revoked ? <div className="revoked-note"><ShieldCheck size={16} />Revoked {formatDate(item.revoked_at ?? "")} · secret destroyed · delivery history retained</div> : <>
            <div className="row-actions"><button className="button secondary small" type="button" onClick={() => setEditing(editing === item.id ? undefined : item.id)}>Configure</button><button className="button secondary small" type="button" disabled={pause.isPending} onClick={() => pause.mutate({ item, active: !item.active })}>{item.active ? <PauseCircle size={15} /> : <PlayCircle size={15} />}{item.active ? "Pause" : "Resume"}</button>{item.signing_mode !== "none" ? <button className="button secondary small" type="button" disabled={rotate.isPending} onClick={() => rotate.mutate(item.id)}><RotateCw size={15} />Rotate secret</button> : null}{item.delivery_format === "github.v3" ? <button className="button secondary small" type="button" onClick={() => setShowSuppressions(showSuppressions === item.id ? undefined : item.id)}><Filter size={15} />Suppressions</button> : null}<button className="button danger small" type="button" disabled={revoke.isPending} onClick={() => confirmRevoke === item.id ? revoke.mutate(item.id) : setConfirmRevoke(item.id)}><Trash2 size={15} />{confirmRevoke === item.id ? "Confirm revoke" : "Revoke"}</button>{confirmRevoke === item.id ? <button className="button secondary small" type="button" onClick={() => setConfirmRevoke(undefined)}>Cancel</button> : null}</div>
            {editing === item.id ? <WebhookEditor orgId={orgId} repoId={repoId} subscription={item} onSaved={() => { setEditing(undefined); void refresh(); }} /> : null}
            {showSuppressions === item.id ? <SuppressionLedger orgId={orgId} subscription={item} /> : null}
          </>}
        </article>;
      })}</div>
    </Panel>
    <DeliveryConsole orgId={orgId} repoId={repoId} />
    {secret ? <SecretDialog secret={secret.value} title={secret.title} onClose={() => setSecret(undefined)} /> : null}
  </>;
}

function WebhookEditor({ orgId, repoId, subscription, onSaved }: { orgId: string; repoId: string; subscription?: WebhookSubscription; onSaved: (result: WebhookSubscription | WebhookSecret) => void }) {
  const inspector = useInspector();
  const defaults = useMemo(() => subscription ? webhookDraft(subscription) : emptyWebhookDraft, [subscription]);
  const [validationError, setValidationError] = useState<string>();
  const { register, handleSubmit, control, formState: { errors } } = useForm<WebhookDraft>({ defaultValues: defaults });
  const format = useWatch({ control, name: "delivery_format" });
  const save = useMutation({
    mutationFn: (draft: WebhookDraft) => {
      const retry = retryFromDraft(draft);
      const content_policy = contentPolicyFromDraft(draft);
      const event_types = draft.delivery_format === "issue-spec.v1" ? draft.event_types : [];
      return subscription
        ? api.updateWebhookSubscription(orgId, subscription.id, { url: draft.url, event_types, delivery_format: draft.delivery_format, signing_mode: signingMode(draft), content_policy, retry, active: subscription.active, expected_version: subscription.representation_version, clear_destination_query: shouldClearDestinationQuery(subscription, draft) })
        : api.createWebhookSubscription(orgId, { repository_id: repoId, url: draft.url, event_types, delivery_format: draft.delivery_format, signing_mode: signingMode(draft), content_policy, retry });
    },
    onSuccess: (result) => { inspector.note(subscription ? "Webhook configuration saved." : "Webhook created. Save the secret before closing the dialog."); onSaved(result); },
    onError: inspector.report,
  });
  const submit = handleSubmit((draft) => { const error = validateWebhookDraft(draft); setValidationError(error); if (!error) save.mutate(draft); });
  return <form className="webhook-editor" onSubmit={submit}>
    <fieldset className="delivery-format-picker"><legend>Delivery contract</legend><label><input type="radio" value="issue-spec.v1" {...register("delivery_format")} /><Bot aria-hidden="true" /><span><strong>Runner intake</strong><small>issue-spec.v1 · bearer envelope for runner serve</small></span></label><label><input type="radio" value="github.v3" {...register("delivery_format")} /><Bell aria-hidden="true" /><span><strong>GitHub-compatible notification</strong><small>github.v3 · content filters and optional HMAC</small></span></label></fieldset>
    <div className="integration-form compact"><Field label="Receiver URL" hint={format === "github.v3" ? "HTTPS in production. A credential query is encrypted and never shown again." : "Runner URLs cannot contain credentials, query, or fragment."} error={errors.url?.message}><TextInput type="url" autoComplete="off" placeholder={format === "github.v3" ? "https://robot.example.test/hook?access_token=…" : "https://runner.example.test/api/v1/runner/webhooks"} {...register("url", { required: "Receiver URL is required", validate: (value) => validateWebhookURL(value, format) })} /></Field>{format === "github.v3" ? <Field label="Signing"><SelectInput {...register("signing_mode")}><option value="none">No request signature</option><option value="hmac-sha256">HMAC SHA-256</option></SelectInput></Field> : <div className="contract-note"><LockKeyhole size={16} /><span><strong>Bearer authentication</strong><small>The runner secret is shown once and never rehydrated.</small></span></div>}<Field label="Maximum attempts" error={errors.max_attempts?.message}><TextInput type="number" min={1} max={100} {...register("max_attempts", { valueAsNumber: true, required: "Retry count is required", min: { value: 1, message: "Use at least one attempt" }, max: { value: 100, message: "Maximum is 100" } })} /></Field><Field label="Initial backoff"><TextInput placeholder="1s" {...register("initial_backoff", { required: true })} /></Field><Field label="Maximum backoff"><TextInput placeholder="5m" {...register("max_backoff", { required: true })} /></Field></div>
    {subscription?.has_destination_query && format === "github.v3" ? <label className="clear-query"><input type="checkbox" {...register("clear_destination_query")} /><span><strong>Remove stored destination credential</strong><small>The encrypted query is intentionally absent from this form. Leave unchecked to preserve it.</small></span></label> : null}
    {format === "issue-spec.v1" ? <OptionPicker legend="Runner events" options={eventOptions} register={register("event_types")} /> : <NotificationPolicyEditor register={register} />}
    {validationError ? <p className="form-alert" role="alert">{validationError}</p> : null}
    <button className="button primary" type="submit" disabled={save.isPending}><Send size={16} />{save.isPending ? "Saving…" : subscription ? "Save route" : "Create route"}</button>
  </form>;
}

function NotificationPolicyEditor({ register }: { register: ReturnType<typeof useForm<WebhookDraft>>["register"] }) {
  return <div className="policy-editor"><div><OptionPicker legend="Issue actions" options={issueActionOptions} register={register("issue_actions")} /><OptionPicker legend="Projected issue kinds" options={issueKindOptions} register={register("issue_kinds")} /></div><div><OptionPicker legend="Comment actions" options={commentActionOptions} register={register("comment_actions")} /><OptionPicker legend="Comment classes" options={commentClassOptions} register={register("comment_classes")} /></div><OptionPicker legend="Actor classes" options={actorClassOptions} register={register("actor_classes")} /><p><ShieldCheck size={15} />Typed workflow classification comes from the server projection, never body text or username convention.</p></div>;
}

function OptionPicker({ legend, options, register }: { legend: string; options: ReadonlyArray<readonly [string, string]>; register: ReturnType<ReturnType<typeof useForm<WebhookDraft>>["register"]> }) {
  return <fieldset className="event-picker"><legend>{legend}</legend>{options.map(([value, label]) => <label key={value}><input type="checkbox" value={value} {...register} /><span><strong>{label}</strong><small>{value}</small></span></label>)}</fieldset>;
}

function SuppressionLedger({ orgId, subscription }: { orgId: string; subscription: WebhookSubscription }) {
  const suppressions = useQuery({ queryKey: ["webhook-suppressions", orgId, subscription.id], queryFn: ({ signal }) => api.webhookSuppressions(orgId, subscription.id, signal) });
  return <section className="suppression-ledger" aria-label={`Suppressions for ${subscription.url}`}><header><div><span className="eyebrow">Policy decisions</span><h3>Suppressed events</h3></div><StatusBadge tone="neutral">{suppressions.data?.suppressions.length ?? 0}</StatusBadge></header>{suppressions.isLoading ? <Loading label="Loading suppressions" /> : null}{suppressions.error ? <ErrorNotice error={suppressions.error} /> : null}{suppressions.data?.suppressions.length === 0 ? <p>No events have been suppressed by this policy.</p> : suppressions.data?.suppressions.map((item) => <article key={item.id}><Filter size={15} aria-hidden="true" /><div><strong>{item.event_type} · {item.action}</strong><small>{item.reason.replaceAll("_", " ")} · {item.issue_kind}{item.comment_class ? ` · ${item.comment_class}` : ""}</small></div><time>{formatDate(item.created_at)}</time></article>)}</section>;
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
  const refreshLedger = () => {
    void deliveries.refetch();
    if (selected) void detail.refetch();
  };
  return <Panel className="delivery-panel" title="Delivery ledger" description="Immutable event identity, exact payload revision and signing-secret version stay fixed across retry and replay."><div className="delivery-toolbar"><div className="delivery-metrics"><span aria-label={`${items.length} recorded`}><strong>{items.length}</strong> recorded</span><span aria-label={`${succeeded} delivered`}><strong>{succeeded}</strong> delivered</span><span aria-label={`${dead} dead ${dead === 1 ? "letter" : "letters"}`}><strong>{dead}</strong> dead {dead === 1 ? "letter" : "letters"}</span></div><button className="icon-button" type="button" aria-label="Refresh deliveries" onClick={refreshLedger}><RefreshCw size={17} /></button></div>{deliveries.isLoading ? <Loading label="Loading delivery ledger" /> : null}{deliveries.error ? <ErrorNotice error={deliveries.error} /> : null}{!deliveries.isLoading && items.length === 0 ? <EmptyState title="No deliveries recorded" description="Delivery records appear after a subscribed repository event enters the outbox." /> : null}<div className="delivery-console"><div className="delivery-list">{items.map((item) => <button aria-label={`${item.event_name} ${item.action} ${item.state}`} className={`delivery-row ${selected === item.id ? "selected" : ""}`} type="button" key={item.id} onClick={() => setSelected(item.id)}><span className={`delivery-state ${item.state}`} aria-hidden="true" /><span><strong>{item.event_name} · {item.action}</strong><small>{item.delivery_format} · #{item.repository_sequence} · {shortID(item.id)}</small></span><StatusBadge tone={deliveryTone(item)}>{item.state}</StatusBadge><time>{formatDate(item.updated_at)}</time></button>)}</div>{selected ? <aside className="delivery-detail" aria-label="Delivery detail">{detail.isLoading ? <Loading label="Reading attempts" /> : null}{detail.error ? <ErrorNotice error={detail.error} /> : null}{detail.data ? <><header><div><span className="eyebrow">Delivery {shortID(detail.data.delivery.id)}</span><h3>{detail.data.delivery.event_name} · {detail.data.delivery.action}</h3></div><StatusBadge tone={deliveryTone(detail.data.delivery)}>{detail.data.delivery.state}</StatusBadge></header><dl className="integration-facts compact"><div><dt>Contract</dt><dd>{detail.data.delivery.delivery_format}</dd></div><div><dt>Event ID</dt><dd className="mono break-word">{detail.data.delivery.event_id}</dd></div><div><dt>Secret version</dt><dd>v{detail.data.delivery.secret_version} · frozen for replay</dd></div><div><dt>Next attempt</dt><dd>{formatDate(detail.data.delivery.next_attempt_at)}</dd></div>{detail.data.delivery.last_error ? <div><dt>Last error</dt><dd>{detail.data.delivery.last_error}</dd></div> : null}</dl><div className="attempts"><h4>Attempts</h4>{detail.data.attempts.length ? detail.data.attempts.map((attempt) => <article key={attempt.id}><span className="attempt-number">{attempt.attempt_number}</span><div><strong>{attempt.response_status ? `HTTP ${attempt.response_status}` : attempt.error ?? "Transport attempt"}</strong><small>{formatDate(attempt.started_at)}{attempt.completed_at ? ` · completed ${formatDate(attempt.completed_at)}` : " · in progress"}</small></div>{attempt.response_status && attempt.response_status >= 200 && attempt.response_status < 300 ? <CheckCircle2 size={17} className="teal-text" /> : <Clock3 size={17} />}</article>) : <p>No attempt has started yet.</p>}</div>{["failed", "dead"].includes(detail.data.delivery.state) ? <button className="button secondary" type="button" disabled={replay.isPending} onClick={() => replay.mutate(detail.data.delivery.id)}><RotateCw size={16} />{replay.isPending ? "Queuing…" : "Replay immutable delivery"}</button> : <p className="immutable-note"><ShieldCheck size={15} />Replay becomes available after a terminal failed or dead-letter outcome.</p>}</> : null}</aside> : null}</div></Panel>;
}

const emptyBindingDraft: SourceBindingDraft = { provider_key: "github", external_repository_id: "", clone_url: "", web_url: "", default_branch: "main" };
const defaultPolicy: WebhookContentPolicy = { issue_actions: ["opened", "edited", "closed", "reopened"], comment_actions: ["created", "edited"], issue_kinds: ["ordinary", "proposal", "design", "implement"], comment_classes: ["human-untyped"], actor_classes: ["human"] };
const emptyWebhookDraft: WebhookDraft = { delivery_format: "issue-spec.v1", url: "", event_types: ["issue_comment.created", "issue_comment.edited"], signing_mode: "hmac-sha256", ...defaultPolicy, clear_destination_query: false, max_attempts: 8, initial_backoff: "1s", max_backoff: "5m" };
function bindingDraft(binding: SourceBinding): SourceBindingDraft { return { provider_key: binding.provider_key, external_repository_id: binding.external_repository_id, clone_url: binding.clone_url, web_url: binding.web_url, default_branch: binding.default_branch }; }
function webhookDraft(item: WebhookSubscription): WebhookDraft { return { delivery_format: item.delivery_format, url: item.url, event_types: item.event_types, signing_mode: item.signing_mode === "bearer" ? "hmac-sha256" : item.signing_mode, ...item.content_policy, clear_destination_query: false, max_attempts: item.retry.max_attempts, initial_backoff: item.retry.initial_backoff, max_backoff: item.retry.max_backoff }; }
export function shouldClearDestinationQuery(item: Pick<WebhookSubscription, "url" | "has_destination_query">, draft: { url: string; clear_destination_query: boolean }) {
  if (draft.clear_destination_query || !item.has_destination_query) return draft.clear_destination_query;
  try {
    const replacement = new URL(draft.url);
    if (replacement.search) return false;
    const current = new URL(item.url);
    current.search = "";
    replacement.search = "";
    return current.toString() !== replacement.toString();
  } catch {
    return false;
  }
}
function retryFromDraft(draft: WebhookDraft): WebhookRetry { return { max_attempts: draft.max_attempts, initial_backoff: draft.initial_backoff, max_backoff: draft.max_backoff }; }
function webhookUpdate(item: WebhookSubscription, active: boolean) { return { url: item.url, event_types: item.event_types, delivery_format: item.delivery_format, signing_mode: item.signing_mode, content_policy: item.content_policy, retry: item.retry, active, expected_version: item.representation_version }; }
function contentPolicyFromDraft(draft: WebhookDraft): WebhookContentPolicy { return { issue_actions: draft.issue_actions, comment_actions: draft.comment_actions, issue_kinds: draft.issue_kinds, comment_classes: draft.comment_classes, actor_classes: draft.actor_classes }; }
function signingMode(draft: WebhookDraft): WebhookSigningMode { return draft.delivery_format === "issue-spec.v1" ? "bearer" : draft.signing_mode; }
function validateWebhookURL(value: string, format: WebhookDeliveryFormat) {
  if (value.trim() !== value || value.includes("#") || value.includes("\\") || (format === "issue-spec.v1" && value.includes("?"))) return format === "github.v3" ? "Use a canonical URL without userinfo, fragment, or backslash" : "Use a canonical URL without credentials, query, or fragment";
  try {
    const parsed = new URL(value);
    if (!parsed.hostname || parsed.username || parsed.password || !["http:", "https:"].includes(parsed.protocol)) return "Use a canonical HTTP(S) receiver URL";
    return true;
  } catch {
    return "Use a valid receiver URL";
  }
}
function validateWebhookDraft(draft: WebhookDraft) {
  if (draft.delivery_format === "issue-spec.v1" && !draft.event_types.length) return "Select at least one runner event.";
  if (draft.delivery_format === "github.v3" && !draft.issue_actions.length && !draft.comment_actions.length) return "Select at least one issue or comment action.";
  if (draft.delivery_format === "github.v3" && !draft.issue_kinds.length) return "Select at least one projected issue kind.";
  if (draft.delivery_format === "github.v3" && !draft.actor_classes.length) return "Select at least one authoritative actor class.";
  if (draft.delivery_format === "github.v3" && draft.comment_actions.length > 0 && !draft.comment_classes.length) return "Select a comment class for comment actions.";
  const initial = durationMilliseconds(draft.initial_backoff); const maximum = durationMilliseconds(draft.max_backoff);
  if (initial === undefined || maximum === undefined) return "Use retry durations such as 500ms, 1s, 5m or 1h.";
  if (maximum < initial) return "Maximum backoff must be greater than or equal to initial backoff.";
  return undefined;
}
function durationMilliseconds(value: string) { const match = /^(\d+)(ms|s|m|h)$/.exec(value.trim()); if (!match) return undefined; const unit = { ms: 1, s: 1_000, m: 60_000, h: 3_600_000 }[match[2] as "ms" | "s" | "m" | "h"]; return Number(match[1]) * unit; }
function policySummary(policy: WebhookContentPolicy) { return [...policy.issue_actions.map((item) => `issue:${item}`), ...policy.comment_actions.map((item) => `comment:${item}`), ...policy.issue_kinds.map((item) => `kind:${item}`), ...policy.comment_classes.map((item) => `class:${item}`)]; }
function signingLabel(mode: WebhookSigningMode) { if (mode === "hmac-sha256") return "HMAC SHA-256"; if (mode === "bearer") return "Bearer secret"; return "Unsigned"; }
function shortID(id: string) { return id.slice(0, 8); }
function formatDate(value: string) { const date = new Date(value); return Number.isNaN(date.getTime()) ? value : new Intl.DateTimeFormat(undefined, { dateStyle: "medium", timeStyle: "short" }).format(date); }
function deliveryTone(item: WebhookDelivery): "neutral" | "teal" | "purple" | "coral" { if (item.state === "succeeded") return "teal"; if (item.state === "failed" || item.state === "dead") return "coral"; if (item.state === "pending" || item.state === "delivering") return "purple"; return "neutral"; }
