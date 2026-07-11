import { useState } from "react";
import { useMutation, useQuery, useQueryClient } from "@tanstack/react-query";
import { useForm } from "react-hook-form";
import { KeyRound, Plus, RefreshCw, Trash2 } from "lucide-react";
import { ErrorNotice, Field, PageHeader, Panel, SecretDialog, StatusBadge, TextInput } from "../app/components";
import { useInspector } from "../app/problem-inspector";
import { api } from "../lib/api/resources";
import { queryKeys } from "./session";

type TokenForm = { name: string; scopes: string };

export function TokensPage() {
  const tokens = useQuery({ queryKey: queryKeys.pats, queryFn: ({ signal }) => api.pats(signal) });
  const [secret, setSecret] = useState("");
  const { register, handleSubmit, reset, formState: { errors } } = useForm<TokenForm>({ defaultValues: { name: "", scopes: "read:user, read:org, issues:read" } });
  const client = useQueryClient();
  const inspector = useInspector();
  const refresh = () => client.invalidateQueries({ queryKey: queryKeys.pats });
  const create = useMutation({ mutationFn: (form: TokenForm) => api.createPAT({ name: form.name, scopes: form.scopes.split(",").map((scope) => scope.trim()).filter(Boolean), expires_at: null }), onSuccess: (value) => { setSecret(value.secret); reset(); void refresh(); }, onError: inspector.report });
  const rotate = useMutation({ mutationFn: api.rotatePAT, onSuccess: (value) => { setSecret(value.secret); void refresh(); }, onError: inspector.report });
  const revoke = useMutation({ mutationFn: api.revokePAT, onSuccess: () => void refresh(), onError: inspector.report });
  return <div className="page"><PageHeader eyebrow="Account / credentials" title="Personal access tokens" description="Issue API credentials are scoped, expiring, revocable, and never recoverable after creation." />
    <div className="two-column token-layout"><Panel title="Create a token" description="Choose only the capabilities this automation needs."><form className="form-grid" onSubmit={handleSubmit((form) => create.mutate(form))}><Field label="Token name" error={errors.name?.message}><TextInput {...register("name", { required: "Name is required" })} /></Field><Field label="Scopes" hint="Comma-separated stable scope names."><TextInput className="input mono" {...register("scopes")} /></Field><button className="button primary" type="submit" disabled={create.isPending}><Plus size={16} />Create token</button></form></Panel>
      <Panel title="Credential hygiene"><div className="editorial-note"><KeyRound size={22} /><p>Store the generated token in an origin-bound issue-spec profile. Never place it in repository configuration or a runner child environment.</p></div></Panel></div>
    <Panel title="Active tokens" description={`${tokens.data?.tokens.length ?? 0} credentials`}>
      {tokens.error ? <ErrorNotice error={tokens.error} /> : null}
      <div className="resource-list">{tokens.data?.tokens.map((token) => <article className="resource-row" key={token.id}><div><strong>{token.name}</strong><span className="mono">{token.token_prefix ?? token.prefix ?? "prefix hidden"}</span></div><div className="scope-list">{token.scopes.map((scope) => <StatusBadge key={scope}>{scope}</StatusBadge>)}</div><div className="row-actions"><button className="icon-button" type="button" onClick={() => rotate.mutate(token.id)} aria-label={`Rotate ${token.name}`}><RefreshCw size={17} /></button><button className="icon-button danger-text" type="button" onClick={() => revoke.mutate(token.id)} aria-label={`Revoke ${token.name}`}><Trash2 size={17} /></button></div></article>)}</div>
    </Panel>
    {secret ? <SecretDialog secret={secret} title="Save this access token" onClose={() => setSecret("")} /> : null}
  </div>;
}
