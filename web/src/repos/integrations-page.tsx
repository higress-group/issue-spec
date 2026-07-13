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
import { useTranslation } from "react-i18next";
import type { TFunction } from "i18next";
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
  "issue_comment.created", "issue_comment.edited", "issue.created", "issue.edited", "issue.closed", "issue.reopened",
] as const;

const issueActionOptions = ["opened", "edited", "closed", "reopened"] as const;
const commentActionOptions = ["created", "edited"] as const;
const issueKindOptions = ["ordinary", "proposal", "design", "implement"] as const;
const commentClassOptions = ["human-untyped", "typed"] as const;
const actorClassOptions = ["human", "automation"] as const;

export function IntegrationsPage({ kind }: { kind: IntegrationKind }) {
  const { t } = useTranslation();
  const { orgId, repoId, repository } = useRepositoryContext();
  const meta = useMeta();
  const access = useQuery({ queryKey: queryKeys.repoContext(orgId), queryFn: ({ signal }) => api.repositoriesContext(orgId, signal) });
  const capability = kind === "source" ? "source_bindings" : "webhooks";
  if (meta.isLoading || repository.isLoading || access.isLoading) return <Loading label={t("integrations.opening")} />;
  if (meta.error) return <ErrorNotice error={meta.error} />;
  if (repository.error) return <ErrorNotice error={repository.error} />;
  if (access.error) return <ErrorNotice error={access.error} />;
  if (!repository.data) return null;
  if (!meta.data?.features[capability]) {
    return <div className="page"><IntegrationHeader kind={kind} repository={repository.data} /><Panel><EmptyState title={t("integrations.unavailableTitle")} description={t("integrations.unavailableDescription")} action={<StatusBadge tone="coral">{t("common.notMounted")}</StatusBadge>} /></Panel></div>;
  }
  const repositoryAccess = access.data?.repositories.find((item) => item.repository.id === repoId);
  const canManage = repositoryAccess?.allowed_actions.includes("integrations.manage") ?? false;
  if (!canManage) return <div className="page integrations-page"><IntegrationHeader kind={kind} repository={repository.data} /><Panel><EmptyState title={t("integrations.requiredTitle")} description={t("integrations.requiredDescription")} action={<StatusBadge tone="coral">{t("common.restricted")}</StatusBadge>} /></Panel></div>;
  return <div className="page integrations-page"><IntegrationHeader kind={kind} repository={repository.data} />{kind === "source" ? <SourceWorkspace orgId={orgId} repoId={repoId} /> : <WebhookWorkspace orgId={orgId} repoId={repoId} />}</div>;
}

function IntegrationHeader({ kind, repository }: { kind: IntegrationKind; repository: AdminRepository }) {
  const { t } = useTranslation();
  return <RepositoryHeader repository={repository} section={kind} title={kind === "source" ? t("integrations.sourceTitle") : t("integrations.webhooksTitle")} description={kind === "source" ? t("integrations.sourceDescription") : t("integrations.webhooksDescription")} />;
}

function SourceWorkspace({ orgId, repoId }: { orgId: string; repoId: string }) {
  const { t, i18n } = useTranslation();
  const inspector = useInspector();
  const client = useQueryClient();
  const binding = useQuery({ queryKey: queryKeys.sourceBinding(orgId, repoId), queryFn: ({ signal }) => api.activeSourceBinding(orgId, repoId, signal) });
  const [confirmDeactivate, setConfirmDeactivate] = useState(false);
  const { register, handleSubmit, reset, formState: { errors } } = useForm<SourceBindingDraft>({ defaultValues: emptyBindingDraft });
  useEffect(() => reset(binding.data ? bindingDraft(binding.data) : emptyBindingDraft), [binding.data, reset]);
  const refresh = () => client.invalidateQueries({ queryKey: queryKeys.sourceBinding(orgId, repoId) });
  const publish = useMutation({
    mutationFn: (draft: SourceBindingDraft) => api.createSourceBinding(orgId, repoId, draft),
    onSuccess: (created) => { inspector.note(t("integrations.bindingActive", { version: created.version })); setConfirmDeactivate(false); void refresh(); },
    onError: (error, draft) => inspector.report(error, draft),
  });
  const deactivate = useMutation({
    mutationFn: () => api.deactivateSourceBinding(orgId, repoId),
    onSuccess: () => { inspector.note(t("integrations.bindingDeactivated")); setConfirmDeactivate(false); void refresh(); },
    onError: inspector.report,
  });
  if (binding.isLoading) return <Loading label={t("integrations.readingBinding")} />;
  return <>
    {binding.error ? <ErrorNotice error={binding.error} /> : null}
    <section className="integration-hero" aria-label={t("integrations.bindingStatus")}>
      <div className="integration-hero-mark"><GitBranch aria-hidden="true" /></div>
      <div><span className="eyebrow">{t("integrations.credentialFree")}</span><h2>{binding.data ? binding.data.external_repository_id : t("integrations.noSource")}</h2><p>{binding.data ? t("integrations.bindingVersion", { provider: binding.data.provider_key, branch: binding.data.default_branch, version: binding.data.version }) : t("integrations.connectHelp")}</p></div>
      <StatusBadge tone={binding.data ? "teal" : "neutral"}>{binding.data ? t("common.active") : t("integrations.unbound")}</StatusBadge>
    </section>
    {binding.data ? <Panel className="binding-summary"><div className="binding-route"><span className="route-node"><Cable size={17} />{t("integrations.localRepository")}</span><span className="route-line" aria-hidden="true" /><a className="route-node external" href={binding.data.web_url} target="_blank" rel="noopener noreferrer"><ExternalLink size={17} />{binding.data.provider_key}</a></div><dl className="integration-facts"><div><dt>{t("integrations.externalRepository")}</dt><dd>{binding.data.external_repository_id}</dd></div><div><dt>{t("integrations.cloneUrl")}</dt><dd className="mono break-word">{binding.data.clone_url}</dd></div><div><dt>{t("common.defaultBranch")}</dt><dd>{binding.data.default_branch}</dd></div><div><dt>{t("integrations.updated")}</dt><dd>{formatDate(binding.data.updated_at, i18n.resolvedLanguage)}</dd></div></dl></Panel> : null}
    <Panel title={binding.data ? t("integrations.publishBinding") : t("integrations.connectSourceIdentity")} description={t("integrations.bindingDescription")}>
      <form className="integration-form" onSubmit={handleSubmit((draft) => publish.mutate(draft))}>
        <Field label={t("integrations.providerKey")} hint={t("integrations.providerHint")} error={errors.provider_key?.message}><TextInput autoComplete="off" {...register("provider_key", { required: t("integrations.providerRequired") })} /></Field>
        <Field label={t("integrations.externalRepositoryId")} hint={t("integrations.externalRepositoryHint")} error={errors.external_repository_id?.message}><TextInput autoComplete="off" {...register("external_repository_id", { required: t("integrations.repositoryIdentityRequired") })} /></Field>
        <Field label={t("integrations.cloneUrl")} hint={t("integrations.cloneHint")} error={errors.clone_url?.message}><TextInput type="url" autoComplete="off" {...register("clone_url", { required: t("integrations.cloneRequired") })} /></Field>
        <Field label={t("integrations.webUrl")} hint={t("integrations.webUrlHint")} error={errors.web_url?.message}><TextInput type="url" autoComplete="off" {...register("web_url", { required: t("integrations.webUrlRequired") })} /></Field>
        <Field label={t("common.defaultBranch")} error={errors.default_branch?.message}><TextInput autoComplete="off" {...register("default_branch", { required: t("integrations.defaultBranchRequired") })} /></Field>
        <div className="integration-form-actions"><button className="button primary" type="submit" disabled={publish.isPending}><GitBranch size={16} />{publish.isPending ? t("integrations.publishing") : binding.data ? t("integrations.publishNewVersion") : t("integrations.connectSource")}</button>{binding.data ? <button className="button danger" type="button" disabled={deactivate.isPending} onClick={() => confirmDeactivate ? deactivate.mutate(undefined) : setConfirmDeactivate(true)}><Trash2 size={16} />{confirmDeactivate ? t("integrations.confirmDeactivation") : t("integrations.deactivate")}</button> : null}{confirmDeactivate ? <button className="button secondary" type="button" onClick={() => setConfirmDeactivate(false)}>{t("integrations.keepActive")}</button> : null}</div>
      </form>
    </Panel>
    <div className="trust-note"><ShieldCheck size={20} /><div><strong>{t("integrations.noCredentialTitle")}</strong><p>{t("integrations.noCredentialHelp")}</p></div></div>
  </>;
}

function WebhookWorkspace({ orgId, repoId }: { orgId: string; repoId: string }) {
  const { t, i18n } = useTranslation();
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
    onSuccess: (_, variables) => { inspector.note(t(variables.active ? "integrations.resumed" : "integrations.paused")); void refresh(); },
    onError: (error, variables) => inspector.report(error, variables),
  });
  const rotate = useMutation({
    mutationFn: (id: string) => api.rotateWebhookSecret(orgId, id),
    onSuccess: (result) => { setSecret({ value: result.secret, title: t("integrations.secretTitle", { version: result.secret_version }) }); inspector.note(t("integrations.secretRotated")); void refresh(); },
    onError: inspector.report,
  });
  const revoke = useMutation({
    mutationFn: (id: string) => api.revokeWebhookSubscription(orgId, id),
    onSuccess: () => { setConfirmRevoke(undefined); setEditing(undefined); inspector.note(t("integrations.revoked")); void refresh(); },
    onError: inspector.report,
  });
  const items = subscriptions.data?.subscriptions ?? [];
  return <>
    <section className="integration-hero webhook-hero" aria-label={t("integrations.overview")}><div className="integration-hero-mark"><Activity aria-hidden="true" /></div><div><span className="eyebrow">{t("integrations.transport")}</span><h2>{t("integrations.activeRoutes", { count: items.filter((item) => item.active).length })}</h2><p>{t("integrations.transportHelp")}</p></div><button className="button primary" type="button" onClick={() => setCreating((value) => !value)}><Plus size={16} />{creating ? t("integrations.closeForm") : t("integrations.newWebhook")}</button></section>
    {creating ? <WebhookEditor orgId={orgId} repoId={repoId} onSaved={(created) => { setCreating(false); if ("secret" in created && created.signing_mode !== "none") setSecret({ value: created.secret, title: t("integrations.secretTitle", { version: created.secret_version }) }); void refresh(); }} /> : null}
    <Panel title={t("integrations.routes")} description={t("integrations.routesHelp")}>
      {subscriptions.isLoading ? <Loading label={t("integrations.loadingRoutes")} /> : null}
      {subscriptions.error ? <ErrorNotice error={subscriptions.error} /> : null}
      {!subscriptions.isLoading && items.length === 0 ? <EmptyState title={t("integrations.noRoutes")} description={t("integrations.noRoutesHelp")} action={<button className="button primary" type="button" onClick={() => setCreating(true)}><Plus size={16} />{t("integrations.createWebhook")}</button>} /> : null}
      <div className="webhook-list">{items.map((item) => {
        const revoked = Boolean(item.revoked_at);
        const lifecycle = revoked ? "revoked" : item.active ? "active" : "paused";
        return <article className={`webhook-card ${lifecycle}`} key={item.id}>
          <header><span className={`pulse ${lifecycle}`} aria-hidden="true" /><div><strong className="break-word">{item.url}</strong><span>{item.delivery_format === "github.v3" ? t("integrations.githubNotification") : t("integrations.runnerIntake")} · v{item.representation_version} · {t("integrations.updatedAt", { date: formatDate(item.updated_at, i18n.resolvedLanguage) })}</span></div><StatusBadge tone={revoked ? "coral" : item.active ? "teal" : "neutral"}>{t(`integrations.lifecycle.${lifecycle}`, { defaultValue: lifecycle })}</StatusBadge></header>
          <div className="route-contract"><StatusBadge tone={item.delivery_format === "github.v3" ? "purple" : "teal"}>{item.delivery_format}</StatusBadge><span>{signingLabel(item.signing_mode, t)}</span>{item.has_destination_query ? <span className="credential-badge"><LockKeyhole size={13} />{t("integrations.encryptedCredential")}</span> : null}</div>
          <div className="event-strip">{item.delivery_format === "github.v3" ? policySummary(item.content_policy).map((filter) => <span key={filter}>{filter}</span>) : item.event_types.map((event) => <span key={event}>{event}</span>)}</div>
          <dl className="retry-line"><div><dt>{t("integrations.attempts")}</dt><dd>{item.retry.max_attempts}</dd></div><div><dt>{t("integrations.firstRetry")}</dt><dd>{item.retry.initial_backoff}</dd></div><div><dt>{t("integrations.retryCeiling")}</dt><dd>{item.retry.max_backoff}</dd></div></dl>
          {revoked ? <div className="revoked-note"><ShieldCheck size={16} />{t("integrations.revokedNote", { date: formatDate(item.revoked_at ?? "", i18n.resolvedLanguage) })}</div> : <>
            <div className="row-actions"><button className="button secondary small" type="button" onClick={() => setEditing(editing === item.id ? undefined : item.id)}>{t("integrations.configure")}</button><button className="button secondary small" type="button" disabled={pause.isPending} onClick={() => pause.mutate({ item, active: !item.active })}>{item.active ? <PauseCircle size={15} /> : <PlayCircle size={15} />}{item.active ? t("integrations.pause") : t("integrations.resume")}</button>{item.signing_mode !== "none" ? <button className="button secondary small" type="button" disabled={rotate.isPending} onClick={() => rotate.mutate(item.id)}><RotateCw size={15} />{t("integrations.rotateSecret")}</button> : null}{item.delivery_format === "github.v3" ? <button className="button secondary small" type="button" onClick={() => setShowSuppressions(showSuppressions === item.id ? undefined : item.id)}><Filter size={15} />{t("integrations.suppressions")}</button> : null}<button className="button danger small" type="button" disabled={revoke.isPending} onClick={() => confirmRevoke === item.id ? revoke.mutate(item.id) : setConfirmRevoke(item.id)}><Trash2 size={15} />{confirmRevoke === item.id ? t("integrations.confirmRevoke") : t("integrations.revoke")}</button>{confirmRevoke === item.id ? <button className="button secondary small" type="button" onClick={() => setConfirmRevoke(undefined)}>{t("integrations.cancel")}</button> : null}</div>
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
  const { t } = useTranslation();
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
    onSuccess: (result) => { inspector.note(t(subscription ? "integrations.configSaved" : "integrations.createdSaveSecret")); onSaved(result); },
    onError: inspector.report,
  });
  const submit = handleSubmit((draft) => { const error = validateWebhookDraft(draft, t); setValidationError(error); if (!error) save.mutate(draft); });
  return <form className="webhook-editor" onSubmit={submit}>
    <fieldset className="delivery-format-picker"><legend>{t("integrations.deliveryContract")}</legend><label><input type="radio" value="issue-spec.v1" {...register("delivery_format")} /><Bot aria-hidden="true" /><span><strong>{t("integrations.runnerIntake")}</strong><small>{t("integrations.runnerContract")}</small></span></label><label><input type="radio" value="github.v3" {...register("delivery_format")} /><Bell aria-hidden="true" /><span><strong>{t("integrations.githubCompatible")}</strong><small>{t("integrations.githubContract")}</small></span></label></fieldset>
    <div className="integration-form compact"><Field label={t("integrations.receiverUrl")} hint={format === "github.v3" ? t("integrations.githubUrlHint") : t("integrations.runnerUrlHint")} error={errors.url?.message}><TextInput type="url" autoComplete="off" placeholder={format === "github.v3" ? "https://robot.example.test/hook?access_token=…" : "https://runner.example.test/api/v1/runner/webhooks"} {...register("url", { required: t("integrations.receiverRequired"), validate: (value) => validateWebhookURL(value, format, t) })} /></Field>{format === "github.v3" ? <Field label={t("integrations.signing")}><SelectInput {...register("signing_mode")}><option value="none">{t("integrations.noSignature")}</option><option value="hmac-sha256">HMAC SHA-256</option></SelectInput></Field> : <div className="contract-note"><LockKeyhole size={16} /><span><strong>{t("integrations.bearerAuth")}</strong><small>{t("integrations.bearerHelp")}</small></span></div>}<Field label={t("integrations.maximumAttempts")} error={errors.max_attempts?.message}><TextInput type="number" min={1} max={100} {...register("max_attempts", { valueAsNumber: true, required: t("integrations.retryRequired"), min: { value: 1, message: t("integrations.atLeastOneAttempt") }, max: { value: 100, message: t("integrations.maximumIs100") } })} /></Field><Field label={t("integrations.initialBackoff")}><TextInput placeholder="1s" {...register("initial_backoff", { required: true })} /></Field><Field label={t("integrations.maximumBackoff")}><TextInput placeholder="5m" {...register("max_backoff", { required: true })} /></Field></div>
    {subscription?.has_destination_query && format === "github.v3" ? <label className="clear-query"><input type="checkbox" {...register("clear_destination_query")} /><span><strong>{t("integrations.removeCredential")}</strong><small>{t("integrations.removeCredentialHelp")}</small></span></label> : null}
    {format === "issue-spec.v1" ? <OptionPicker legend={t("integrations.runnerEvents")} options={eventOptions} register={register("event_types")} /> : <NotificationPolicyEditor register={register} />}
    {validationError ? <p className="form-alert" role="alert">{validationError}</p> : null}
    <button className="button primary" type="submit" disabled={save.isPending}><Send size={16} />{save.isPending ? t("integrations.saving") : subscription ? t("integrations.saveRoute") : t("integrations.createRoute")}</button>
  </form>;
}

function NotificationPolicyEditor({ register }: { register: ReturnType<typeof useForm<WebhookDraft>>["register"] }) {
  const { t } = useTranslation();
  return <div className="policy-editor"><div><OptionPicker legend={t("integrations.issueActions")} options={issueActionOptions} register={register("issue_actions")} /><OptionPicker legend={t("integrations.issueKinds")} options={issueKindOptions} register={register("issue_kinds")} /></div><div><OptionPicker legend={t("integrations.commentActions")} options={commentActionOptions} register={register("comment_actions")} /><OptionPicker legend={t("integrations.commentClasses")} options={commentClassOptions} register={register("comment_classes")} /></div><OptionPicker legend={t("integrations.actorClasses")} options={actorClassOptions} register={register("actor_classes")} /><p><ShieldCheck size={15} />{t("integrations.classificationHelp")}</p></div>;
}

function OptionPicker({ legend, options, register }: { legend: string; options: ReadonlyArray<string>; register: ReturnType<ReturnType<typeof useForm<WebhookDraft>>["register"]> }) {
  const { t } = useTranslation();
  return <fieldset className="event-picker"><legend>{legend}</legend>{options.map((value) => <label key={value}><input type="checkbox" value={value} {...register} /><span><strong>{t(`integrations.option.${optionKey(value)}`)}</strong><small>{value}</small></span></label>)}</fieldset>;
}

function SuppressionLedger({ orgId, subscription }: { orgId: string; subscription: WebhookSubscription }) {
  const { t, i18n } = useTranslation();
  const suppressions = useQuery({ queryKey: ["webhook-suppressions", orgId, subscription.id], queryFn: ({ signal }) => api.webhookSuppressions(orgId, subscription.id, signal) });
  return <section className="suppression-ledger" aria-label={t("integrations.suppressionLabel", { url: subscription.url })}><header><div><span className="eyebrow">{t("integrations.policyDecisions")}</span><h3>{t("integrations.suppressedEvents")}</h3></div><StatusBadge tone="neutral">{suppressions.data?.suppressions.length ?? 0}</StatusBadge></header>{suppressions.isLoading ? <Loading label={t("integrations.loadingSuppressions")} /> : null}{suppressions.error ? <ErrorNotice error={suppressions.error} /> : null}{suppressions.data?.suppressions.length === 0 ? <p>{t("integrations.noSuppressions")}</p> : suppressions.data?.suppressions.map((item) => <article key={item.id}><Filter size={15} aria-hidden="true" /><div><strong>{item.event_type} · {item.action}</strong><small>{item.reason.replaceAll("_", " ")} · {item.issue_kind}{item.comment_class ? ` · ${item.comment_class}` : ""}</small></div><time>{formatDate(item.created_at, i18n.resolvedLanguage)}</time></article>)}</section>;
}

function DeliveryConsole({ orgId, repoId }: { orgId: string; repoId: string }) {
  const { t, i18n } = useTranslation();
  const inspector = useInspector();
  const client = useQueryClient();
  const [selected, setSelected] = useState<string>();
  const deliveries = useQuery({ queryKey: queryKeys.deliveries(orgId, repoId), queryFn: ({ signal }) => api.webhookDeliveries(orgId, repoId, signal) });
  const detail = useQuery({ queryKey: queryKeys.delivery(orgId, repoId, selected ?? ""), queryFn: ({ signal }) => api.webhookDelivery(orgId, repoId, selected ?? "", signal), enabled: Boolean(selected) });
  const replay = useMutation({ mutationFn: (id: string) => api.redeliverWebhookDelivery(orgId, repoId, id), onSuccess: (item) => { inspector.note(t("integrations.replayQueued", { id: shortID(item.id) })); void client.invalidateQueries({ queryKey: queryKeys.deliveries(orgId, repoId) }); void client.invalidateQueries({ queryKey: queryKeys.delivery(orgId, repoId, item.id) }); }, onError: inspector.report });
  const items = deliveries.data?.deliveries ?? [];
  const succeeded = items.filter((item) => item.state === "succeeded").length;
  const dead = items.filter((item) => item.state === "dead").length;
  const refreshLedger = () => {
    void deliveries.refetch();
    if (selected) void detail.refetch();
  };
  return <Panel className="delivery-panel" title={t("integrations.ledger")} description={t("integrations.ledgerHelp")}><div className="delivery-toolbar"><div className="delivery-metrics"><span aria-label={t("integrations.recorded", { count: items.length })}><strong>{items.length}</strong> {t("integrations.recordedLabel")}</span><span aria-label={t("integrations.delivered", { count: succeeded })}><strong>{succeeded}</strong> {t("integrations.deliveredLabel")}</span><span aria-label={t(dead === 1 ? "integrations.deadLetter" : "integrations.deadLetters", { count: dead })}><strong>{dead}</strong> {t(dead === 1 ? "integrations.deadLetterLabel" : "integrations.deadLettersLabel")}</span></div><button className="icon-button" type="button" aria-label={t("integrations.refreshDeliveries")} onClick={refreshLedger}><RefreshCw size={17} /></button></div>{deliveries.isLoading ? <Loading label={t("integrations.loadingLedger")} /> : null}{deliveries.error ? <ErrorNotice error={deliveries.error} /> : null}{!deliveries.isLoading && items.length === 0 ? <EmptyState title={t("integrations.noDeliveries")} description={t("integrations.noDeliveriesHelp")} /> : null}<div className="delivery-console"><div className="delivery-list">{items.map((item) => { const event = t(`integrations.option.${item.event_name}`, { defaultValue: item.event_name }); const action = t(`integrations.deliveryAction.${item.action}`, { defaultValue: item.action }); const state = t(`integrations.lifecycle.${item.state}`, { defaultValue: item.state }); return <button aria-label={t("integrations.deliveryRowAria", { event, action, state })} className={`delivery-row ${selected === item.id ? "selected" : ""}`} type="button" key={item.id} onClick={() => setSelected(item.id)}><span className={`delivery-state ${item.state}`} aria-hidden="true" /><span><strong>{event} · {action}</strong><small>{item.delivery_format} · #{item.repository_sequence} · {shortID(item.id)}</small></span><StatusBadge tone={deliveryTone(item)}>{state}</StatusBadge><time>{formatDate(item.updated_at, i18n.resolvedLanguage)}</time></button>; })}</div>{selected ? <aside className="delivery-detail" aria-label={t("integrations.deliveryDetail")}>{detail.isLoading ? <Loading label={t("integrations.readingAttempts")} /> : null}{detail.error ? <ErrorNotice error={detail.error} /> : null}{detail.data ? <><header><div><span className="eyebrow">{t("integrations.delivery", { id: shortID(detail.data.delivery.id) })}</span><h3>{t(`integrations.option.${detail.data.delivery.event_name}`, { defaultValue: detail.data.delivery.event_name })} · {t(`integrations.deliveryAction.${detail.data.delivery.action}`, { defaultValue: detail.data.delivery.action })}</h3></div><StatusBadge tone={deliveryTone(detail.data.delivery)}>{t(`integrations.lifecycle.${detail.data.delivery.state}`, { defaultValue: detail.data.delivery.state })}</StatusBadge></header><dl className="integration-facts compact"><div><dt>{t("integrations.contract")}</dt><dd>{detail.data.delivery.delivery_format}</dd></div><div><dt>{t("integrations.eventId")}</dt><dd className="mono break-word">{detail.data.delivery.event_id}</dd></div><div><dt>{t("integrations.secretVersion")}</dt><dd>{t("integrations.frozenReplay", { version: detail.data.delivery.secret_version })}</dd></div><div><dt>{t("integrations.nextAttempt")}</dt><dd>{formatDate(detail.data.delivery.next_attempt_at, i18n.resolvedLanguage)}</dd></div>{detail.data.delivery.last_error ? <div><dt>{t("integrations.lastError")}</dt><dd>{detail.data.delivery.last_error}</dd></div> : null}</dl><div className="attempts"><h4>{t("integrations.attemptHistory")}</h4>{detail.data.attempts.length ? detail.data.attempts.map((attempt) => <article key={attempt.id}><span className="attempt-number">{attempt.attempt_number}</span><div><strong>{attempt.response_status ? `HTTP ${attempt.response_status}` : attempt.error ?? t("integrations.transportAttempt")}</strong><small>{formatDate(attempt.started_at, i18n.resolvedLanguage)} · {attempt.completed_at ? t("integrations.completed", { date: formatDate(attempt.completed_at, i18n.resolvedLanguage) }) : t("integrations.inProgress")}</small></div>{attempt.response_status && attempt.response_status >= 200 && attempt.response_status < 300 ? <CheckCircle2 size={17} className="teal-text" /> : <Clock3 size={17} />}</article>) : <p>{t("integrations.noAttempts")}</p>}</div>{["failed", "dead"].includes(detail.data.delivery.state) ? <button className="button secondary" type="button" disabled={replay.isPending} onClick={() => replay.mutate(detail.data.delivery.id)}><RotateCw size={16} />{replay.isPending ? t("integrations.queuing") : t("integrations.replay")}</button> : <p className="immutable-note"><ShieldCheck size={15} />{t("integrations.replayHelp")}</p>}</> : null}</aside> : null}</div></Panel>;
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
function validateWebhookURL(value: string, format: WebhookDeliveryFormat, t: TFunction) {
  if (value.trim() !== value || value.includes("#") || value.includes("\\") || (format === "issue-spec.v1" && value.includes("?"))) return t(format === "github.v3" ? "integrations.validation.canonicalGithub" : "integrations.validation.canonicalRunner");
  try {
    const parsed = new URL(value);
    if (!parsed.hostname || parsed.username || parsed.password || !["http:", "https:"].includes(parsed.protocol)) return t("integrations.validation.canonicalHttp");
    return true;
  } catch {
    return t("integrations.validation.validUrl");
  }
}
function validateWebhookDraft(draft: WebhookDraft, t: TFunction) {
  if (draft.delivery_format === "issue-spec.v1" && !draft.event_types.length) return t("integrations.validation.runnerEvent");
  if (draft.delivery_format === "github.v3" && !draft.issue_actions.length && !draft.comment_actions.length) return t("integrations.validation.issueAction");
  if (draft.delivery_format === "github.v3" && !draft.issue_kinds.length) return t("integrations.validation.issueKind");
  if (draft.delivery_format === "github.v3" && !draft.actor_classes.length) return t("integrations.validation.actorClass");
  if (draft.delivery_format === "github.v3" && draft.comment_actions.length > 0 && !draft.comment_classes.length) return t("integrations.validation.commentClass");
  const initial = durationMilliseconds(draft.initial_backoff); const maximum = durationMilliseconds(draft.max_backoff);
  if (initial === undefined || maximum === undefined) return t("integrations.validation.retryDuration");
  if (maximum < initial) return t("integrations.validation.backoffOrder");
  return undefined;
}
function durationMilliseconds(value: string) { const match = /^(\d+)(ms|s|m|h)$/.exec(value.trim()); if (!match) return undefined; const unit = { ms: 1, s: 1_000, m: 60_000, h: 3_600_000 }[match[2] as "ms" | "s" | "m" | "h"]; return Number(match[1]) * unit; }
function policySummary(policy: WebhookContentPolicy) { return [...policy.issue_actions.map((item) => `issue:${item}`), ...policy.comment_actions.map((item) => `comment:${item}`), ...policy.issue_kinds.map((item) => `kind:${item}`), ...policy.comment_classes.map((item) => `class:${item}`)]; }
function signingLabel(mode: WebhookSigningMode, t: TFunction) { if (mode === "hmac-sha256") return "HMAC SHA-256"; if (mode === "bearer") return t("integrations.signingBearer"); return t("integrations.unsigned"); }
function shortID(id: string) { return id.slice(0, 8); }
function formatDate(value: string, locale?: string) { const date = new Date(value); return Number.isNaN(date.getTime()) ? value : new Intl.DateTimeFormat(locale, { dateStyle: "medium", timeStyle: "short" }).format(date); }
function optionKey(value: string) { return value.replaceAll(".", "_").replaceAll("-", "_"); }
function deliveryTone(item: WebhookDelivery): "neutral" | "teal" | "purple" | "coral" { if (item.state === "succeeded") return "teal"; if (item.state === "failed" || item.state === "dead") return "coral"; if (item.state === "pending" || item.state === "delivering") return "purple"; return "neutral"; }
