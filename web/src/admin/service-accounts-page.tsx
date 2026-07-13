import { useState } from "react";
import { useMutation, useQuery, useQueryClient } from "@tanstack/react-query";
import { useForm } from "react-hook-form";
import { Bot, KeyRound, Plus, RefreshCw, Search, Trash2 } from "lucide-react";
import { useParams } from "react-router-dom";
import { ErrorNotice, Field, PageHeader, Panel, SecretDialog, StatusBadge, TextInput } from "../app/components";
import { useInspector } from "../app/problem-inspector";
import { api } from "../lib/api/resources";
import { queryKeys } from "../auth/session";
import { useTranslation } from "react-i18next";

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
    <Panel title={t("serviceAccounts.accounts")}>{accounts.error ? <ErrorNotice error={accounts.error} /> : null}<div className="resource-list">{accounts.data?.service_accounts.map((account) => <article className="resource-row" key={account.id}><Bot size={19} /><div><strong>{account.name}</strong><span className="mono">@{account.login}</span></div><StatusBadge tone={account.disabled_at ? "coral" : "teal"}>{account.disabled_at ? t("common.disabled") : t("common.active")}</StatusBadge>{!account.disabled_at ? <button className="button danger small" type="button" onClick={() => disable.mutate(account.id)}><Trash2 size={15} />{t("serviceAccounts.disable")}</button> : null}</article>)}</div></Panel>
  </div>;
}

type ManagedToken = { id: string; name: string; token_prefix?: string; scopes?: string[]; revoked_at?: string };

export function ManagedTokensPage() {
  const { t } = useTranslation();
  const { orgId = "" } = useParams();
  const [login, setLogin] = useState("");
  const [target, setTarget] = useState<{ id: string; login: string }>();
  const [secret, setSecret] = useState("");
  const inspector = useInspector();
  const client = useQueryClient();
  const resolve = useMutation({ mutationFn: () => api.userCandidates(orgId, "managed_pat", login, "exact"), onSuccess: (data) => setTarget(data.users[0] ? { id: data.users[0].id, login: data.users[0].login } : undefined), onError: inspector.report });
  const tokens = useQuery({ queryKey: ["managed-pats", orgId, target?.id], queryFn: ({ signal }) => api.managedPATs(orgId, target!.id, signal), enabled: Boolean(target) });
  const create = useMutation({ mutationFn: () => api.createManagedPAT(orgId, target!.id, { name: "automation", scopes: ["issues:read", "issues:write"] }), onSuccess: (value: any) => { setSecret(value.secret); void client.invalidateQueries({ queryKey: ["managed-pats", orgId, target?.id] }); }, onError: inspector.report });
  const rotate = useMutation({ mutationFn: (id: string) => api.rotateManagedPAT(orgId, id), onSuccess: (value) => { setSecret(value.secret); void client.invalidateQueries({ queryKey: ["managed-pats", orgId, target?.id] }); }, onError: inspector.report });
  const revoke = useMutation({ mutationFn: (id: string) => api.revokeManagedPAT(orgId, id), onSuccess: () => void client.invalidateQueries({ queryKey: ["managed-pats", orgId, target?.id] }), onError: inspector.report });
  return <div className="page"><PageHeader eyebrow={t("managedTokens.eyebrow")} title={t("managedTokens.title")} description={t("managedTokens.description")} />
    <Panel title={t("managedTokens.chooseSubject")}><div className="inline-form"><Field label={t("managedTokens.exactLogin")}><TextInput value={login} onChange={(event) => { setLogin(event.target.value); setTarget(undefined); }} /></Field><button className="button secondary" type="button" onClick={() => resolve.mutate(undefined)}><Search size={16} />{t("managedTokens.resolve")}</button>{target ? <StatusBadge tone="teal">@{target.login}</StatusBadge> : null}<button className="button primary" type="button" disabled={!target} onClick={() => create.mutate(undefined)}><KeyRound size={16} />{t("managedTokens.create")}</button></div></Panel>
    <Panel title={t("managedTokens.credentials")}><div className="resource-list">{(tokens.data?.tokens as ManagedToken[] | undefined)?.map((token) => <article className="resource-row" key={token.id}><div><strong>{token.name}</strong><span className="mono">{token.token_prefix}</span></div><div className="scope-list">{token.scopes?.map((scope) => <StatusBadge key={scope}>{scope}</StatusBadge>)}</div><div className="row-actions"><button className="icon-button" type="button" onClick={() => rotate.mutate(token.id)} aria-label={t("managedTokens.rotate", { name: token.name })}><RefreshCw size={16} /></button><button className="icon-button danger-text" type="button" onClick={() => revoke.mutate(token.id)} aria-label={t("managedTokens.revoke", { name: token.name })}><Trash2 size={16} /></button></div></article>)}</div></Panel>
    {secret ? <SecretDialog secret={secret} title={t("managedTokens.saveTitle")} onClose={() => setSecret("")} /> : null}
  </div>;
}
