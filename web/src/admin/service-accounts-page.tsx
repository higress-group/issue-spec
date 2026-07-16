import { useState } from "react";
import { useMutation, useQuery, useQueryClient } from "@tanstack/react-query";
import { useForm, useWatch } from "react-hook-form";
import { Bot, KeyRound, Plus, RefreshCw, Search, ShieldCheck, Trash2 } from "lucide-react";
import { useParams } from "react-router-dom";
import { ErrorNotice, Field, PageHeader, Panel, SecretDialog, SelectInput, StatusBadge, TextInput } from "../app/components";
import { useInspector } from "../app/problem-inspector";
import { api } from "../lib/api/resources";
import { queryKeys } from "../auth/session";
import { useTranslation } from "react-i18next";
import type { TFunction } from "i18next";

export function ServiceAccountsPage() {
  const { t } = useTranslation();
  const { orgId = "" } = useParams();
  const accounts = useQuery({ queryKey: queryKeys.serviceAccounts(orgId), queryFn: ({ signal }) => api.serviceAccounts(orgId, signal) });
  const { register, handleSubmit, reset } = useForm<{ name: string }>();
  const inspector = useInspector();
  const client = useQueryClient();
  const refresh = () => client.invalidateQueries({ queryKey: queryKeys.serviceAccounts(orgId) });
  const create = useMutation({ mutationFn: ({ name }: { name: string }) => api.createServiceAccount(orgId, name), onSuccess: () => { reset(); void refresh(); }, onError: inspector.report });
  const disable = useMutation({ mutationFn: (id: string) => api.disableServiceAccount(orgId, id), onSuccess: () => void refresh(), onError: inspector.report });
  return <div className="page"><PageHeader eyebrow={t("serviceAccounts.eyebrow")} title={t("serviceAccounts.title")} description={t("serviceAccounts.description")} />
    <div className="two-column"><Panel title={t("serviceAccounts.createTitle")}><form className="form-grid" onSubmit={handleSubmit((form) => create.mutate(form))}><Field label={t("serviceAccounts.accountName")}><TextInput {...register("name", { required: true })} /></Field><button className="button primary" type="submit"><Plus size={16} />{t("serviceAccounts.create")}</button></form></Panel><Panel title={t("serviceAccounts.boundaryTitle")}><div className="editorial-note"><Bot size={22} /><p>{t("serviceAccounts.boundaryDescription")}</p></div></Panel></div>
    <Panel title={t("serviceAccounts.accounts")}>{accounts.error ? <ErrorNotice error={accounts.error} /> : null}<div className="resource-list">{(accounts.data?.service_accounts ?? []).map((account) => <article className="resource-row" key={account.id}><Bot size={19} /><div><strong>{account.name}</strong><span className="mono">@{account.login}</span></div><StatusBadge tone={account.disabled_at ? "coral" : "teal"}>{account.disabled_at ? t("common.disabled") : t("common.active")}</StatusBadge>{!account.disabled_at ? <button className="button danger small" type="button" onClick={() => disable.mutate(account.id)}><Trash2 size={15} />{t("serviceAccounts.disable")}</button> : null}</article>)}</div></Panel>
  </div>;
}

type ManagedToken = { id: string; name: string; token_prefix?: string; scopes?: string[]; repository_ids?: string[]; revoked_at?: string };
type ManagedTokenForm = { name: string; scopes: string; repository: string };

const runnerScopes = "read:user, issues:read, issues:write, evidence:write";

export function ManagedTokensPage() {
  const { t } = useTranslation();
  const { orgId = "" } = useParams();
  const [login, setLogin] = useState("");
  const [target, setTarget] = useState<{ id: string; login: string }>();
  const [secret, setSecret] = useState("");
  const inspector = useInspector();
  const client = useQueryClient();
  const repositories = useQuery({ queryKey: ["admin-repositories", orgId], queryFn: ({ signal }) => api.repositories(orgId, signal) });
  const { register, handleSubmit, reset, setValue, getValues, control, formState: { errors } } = useForm<ManagedTokenForm>({ defaultValues: { name: "", scopes: "issues:read, issues:write", repository: "" } });
  const selectedScopes = managedScopes(useWatch({ control, name: "scopes" }));
  const resolve = useMutation({ mutationFn: () => api.userCandidates(orgId, "managed_pat", login, "exact"), onSuccess: (data) => setTarget(data.users[0] ? { id: data.users[0].id, login: data.users[0].login } : undefined), onError: inspector.report });
  const tokens = useQuery({ queryKey: ["managed-pats", orgId, target?.id], queryFn: ({ signal }) => api.managedPATs(orgId, target!.id, signal), enabled: Boolean(target) });
  const create = useMutation({ mutationFn: (form: ManagedTokenForm) => api.createManagedPAT(orgId, target!.id, {
    name: form.name,
    scopes: managedScopes(form.scopes),
    repository_ids: form.repository ? [form.repository] : [],
  }), onSuccess: (value: any) => { setSecret(value.secret); reset(); void client.invalidateQueries({ queryKey: ["managed-pats", orgId, target?.id] }); }, onError: inspector.report });
  const rotate = useMutation({ mutationFn: (id: string) => api.rotateManagedPAT(orgId, id), onSuccess: (value) => { setSecret(value.secret); void client.invalidateQueries({ queryKey: ["managed-pats", orgId, target?.id] }); }, onError: inspector.report });
  const revoke = useMutation({ mutationFn: (id: string) => api.revokeManagedPAT(orgId, id), onSuccess: () => void client.invalidateQueries({ queryKey: ["managed-pats", orgId, target?.id] }), onError: inspector.report });
  return <div className="page"><PageHeader eyebrow={t("managedTokens.eyebrow")} title={t("managedTokens.title")} description={t("managedTokens.description")} />
    <Panel title={t("managedTokens.chooseSubject")} description={t("managedTokens.subjectHelp")}><div className="inline-form"><Field label={t("managedTokens.exactLogin")}><TextInput value={login} onChange={(event) => { setLogin(event.target.value); setTarget(undefined); }} /></Field><button className="button secondary" type="button" disabled={!login.trim() || resolve.isPending} onClick={() => resolve.mutate(undefined)}><Search size={16} />{t("managedTokens.resolve")}</button>{target ? <StatusBadge tone="teal">@{target.login}</StatusBadge> : null}</div></Panel>
    <div className="two-column token-layout"><Panel title={t("managedTokens.createTitle")} description={t("managedTokens.createDescription")}><form className="form-grid" onSubmit={handleSubmit((form) => create.mutate(form))}>
      <Field label={t("managedTokens.tokenName")} error={errors.name?.message}><TextInput {...register("name", { required: t("managedTokens.nameRequired") })} /></Field>
      <Field label={t("managedTokens.scopes")} hint={t("managedTokens.scopesHint")}><TextInput className="input mono" {...register("scopes")} /><div className="scope-list token-scope-preview">{selectedScopes.map((scope) => <StatusBadge key={scope}>{scope}</StatusBadge>)}</div></Field>
      <Field label={t("managedTokens.repositoryAccess")} hint={t("managedTokens.repositoryHint")} error={errors.repository?.message}><SelectInput aria-label={t("managedTokens.repositoryAccess")} {...register("repository", { validate: (value) => !requiresRepository(managedScopes(getValues("scopes"))) || value !== "" || t("managedTokens.runnerRepositoryRequired") })}><option value="">{t("managedTokens.allRepositories")}</option>{(repositories.data?.repositories ?? []).map((repository) => <option key={repository.id} value={repository.id}>{repository.name}</option>)}</SelectInput></Field>
      <div className="button-row"><button className="button secondary" type="button" onClick={() => { if (!getValues("name")) setValue("name", "runner"); setValue("scopes", runnerScopes, { shouldValidate: true }); }}><ShieldCheck size={16} />{t("managedTokens.runnerPreset")}</button><button className="button primary" type="submit" disabled={!target || create.isPending}><KeyRound size={16} />{t("managedTokens.create")}</button></div>
    </form></Panel><Panel title={t("managedTokens.runnerBoundaryTitle")}><div className="editorial-note"><Bot size={22} /><p>{t("managedTokens.runnerBoundaryDescription")}</p></div></Panel></div>
    <Panel title={t("managedTokens.credentials")}>{repositories.error ? <ErrorNotice error={repositories.error} /> : null}{tokens.error ? <ErrorNotice error={tokens.error} /> : null}<div className="resource-list">{(tokens.data?.tokens as ManagedToken[] | undefined)?.map((token) => <article className="resource-row" key={token.id}><div><strong>{token.name}</strong><span className="mono">{token.token_prefix}</span><small>{managedTokenBoundary(token, repositories.data?.repositories ?? [], t)}</small></div><div className="scope-list">{token.scopes?.map((scope) => <StatusBadge key={scope}>{scope}</StatusBadge>)}</div><div className="row-actions"><button className="icon-button" type="button" onClick={() => rotate.mutate(token.id)} aria-label={t("managedTokens.rotate", { name: token.name })}><RefreshCw size={16} /></button><button className="icon-button danger-text" type="button" onClick={() => revoke.mutate(token.id)} aria-label={t("managedTokens.revoke", { name: token.name })}><Trash2 size={16} /></button></div></article>)}</div></Panel>
    {secret ? <SecretDialog secret={secret} title={t("managedTokens.saveTitle")} onClose={() => setSecret("")} /> : null}
  </div>;
}

function managedTokenBoundary(token: ManagedToken, repositories: Array<{ id: string; name: string }>, t: TFunction) {
  if (!token.repository_ids?.length) return t("managedTokens.allRepositories");
  const names = token.repository_ids.map((id) => repositories.find((repository) => repository.id === id)?.name).filter(Boolean);
  return names.length === token.repository_ids.length ? t("managedTokens.restrictedTo", { repositories: names.join(", ") }) : t("managedTokens.restrictedRepositories", { count: token.repository_ids.length });
}

function managedScopes(value: string | undefined) {
  return (value ?? "").split(",").map((scope) => scope.trim()).filter(Boolean);
}

function requiresRepository(scopes: string[]) {
  return scopes.includes("evidence:write") || scopes.includes("runner:delegate");
}
