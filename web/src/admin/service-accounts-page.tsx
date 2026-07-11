import { useState } from "react";
import { useMutation, useQuery, useQueryClient } from "@tanstack/react-query";
import { useForm } from "react-hook-form";
import { Bot, KeyRound, Plus, RefreshCw, Search, Trash2 } from "lucide-react";
import { useParams } from "react-router-dom";
import { ErrorNotice, Field, PageHeader, Panel, SecretDialog, StatusBadge, TextInput } from "../app/components";
import { useInspector } from "../app/problem-inspector";
import { api } from "../lib/api/resources";
import { queryKeys } from "../auth/session";

export function ServiceAccountsPage() {
  const { orgId = "" } = useParams();
  const accounts = useQuery({ queryKey: queryKeys.serviceAccounts(orgId), queryFn: ({ signal }) => api.serviceAccounts(orgId, signal) });
  const { register, handleSubmit, reset } = useForm<{ name: string }>();
  const inspector = useInspector();
  const client = useQueryClient();
  const refresh = () => client.invalidateQueries({ queryKey: queryKeys.serviceAccounts(orgId) });
  const create = useMutation({ mutationFn: ({ name }: { name: string }) => api.createServiceAccount(orgId, name), onSuccess: () => { reset(); void refresh(); }, onError: inspector.report });
  const disable = useMutation({ mutationFn: (id: string) => api.disableServiceAccount(orgId, id), onSuccess: () => void refresh(), onError: inspector.report });
  return <div className="page"><PageHeader eyebrow="Organization / automation" title="Service accounts" description="Non-human identities remain explicit, organization-bound, and independently disableable." />
    <div className="two-column"><Panel title="Create service account"><form className="form-grid" onSubmit={handleSubmit((form) => create.mutate(form))}><Field label="Account name"><TextInput {...register("name", { required: true })} /></Field><button className="button primary" type="submit"><Plus size={16} />Create account</button></form></Panel><Panel title="Credential boundary"><div className="editorial-note"><Bot size={22} /><p>Creating an account does not create a PAT. Issue credentials are minted separately with an explicit repository and scope cap.</p></div></Panel></div>
    <Panel title="Accounts">{accounts.error ? <ErrorNotice error={accounts.error} /> : null}<div className="resource-list">{accounts.data?.service_accounts.map((account) => <article className="resource-row" key={account.id}><Bot size={19} /><div><strong>{account.name}</strong><span className="mono">@{account.login}</span></div><StatusBadge tone={account.disabled_at ? "coral" : "teal"}>{account.disabled_at ? "disabled" : "active"}</StatusBadge>{!account.disabled_at ? <button className="button danger small" type="button" onClick={() => disable.mutate(account.id)}><Trash2 size={15} />Disable</button> : null}</article>)}</div></Panel>
  </div>;
}

type ManagedToken = { id: string; name: string; token_prefix?: string; scopes?: string[]; revoked_at?: string };

export function ManagedTokensPage() {
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
  return <div className="page"><PageHeader eyebrow="Organization / credentials" title="Managed PATs" description="Administrators can mint scoped tokens only for active organization members or enabled service accounts." />
    <Panel title="Choose a credential subject"><div className="inline-form"><Field label="Exact local login"><TextInput value={login} onChange={(event) => { setLogin(event.target.value); setTarget(undefined); }} /></Field><button className="button secondary" type="button" onClick={() => resolve.mutate(undefined)}><Search size={16} />Resolve</button>{target ? <StatusBadge tone="teal">@{target.login}</StatusBadge> : null}<button className="button primary" type="button" disabled={!target} onClick={() => create.mutate(undefined)}><KeyRound size={16} />Create scoped PAT</button></div></Panel>
    <Panel title="Managed credentials"><div className="resource-list">{(tokens.data?.tokens as ManagedToken[] | undefined)?.map((token) => <article className="resource-row" key={token.id}><div><strong>{token.name}</strong><span className="mono">{token.token_prefix}</span></div><div className="scope-list">{token.scopes?.map((scope) => <StatusBadge key={scope}>{scope}</StatusBadge>)}</div><div className="row-actions"><button className="icon-button" type="button" onClick={() => rotate.mutate(token.id)} aria-label={`Rotate ${token.name}`}><RefreshCw size={16} /></button><button className="icon-button danger-text" type="button" onClick={() => revoke.mutate(token.id)} aria-label={`Revoke ${token.name}`}><Trash2 size={16} /></button></div></article>)}</div></Panel>
    {secret ? <SecretDialog secret={secret} title="Save this managed token" onClose={() => setSecret("")} /> : null}
  </div>;
}
