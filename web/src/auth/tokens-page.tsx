import { useState } from "react";
import { useMutation, useQueries, useQuery, useQueryClient } from "@tanstack/react-query";
import { useForm } from "react-hook-form";
import { KeyRound, Plus, RefreshCw, ShieldCheck, Trash2 } from "lucide-react";
import { EmptyState, ErrorNotice, Field, PageHeader, Panel, SecretDialog, SelectInput, StatusBadge, TextInput } from "../app/components";
import { useInspector } from "../app/problem-inspector";
import { api } from "../lib/api/resources";
import type { PAT } from "../lib/api/types";
import { queryKeys, useCurrentContext } from "./session";

type TokenForm = { name: string; scopes: string; repository: string };
type RepositoryOption = { organizationId: string; repositoryId: string; label: string };

const runnerScopes = "read:user, issues:read, issues:write, runner:delegate";

export function TokensPage() {
  const tokens = useQuery({ queryKey: queryKeys.pats, queryFn: ({ signal }) => api.pats(signal) });
  const context = useCurrentContext();
  const repositoryQueries = useQueries({ queries: (context.data?.organizations ?? []).map((organization) => ({
    queryKey: queryKeys.repoContext(organization.id),
    queryFn: ({ signal }: { signal: AbortSignal }) => api.repositoriesContext(organization.id, signal),
    staleTime: 60_000,
  })) });
  const repositoryOptions: RepositoryOption[] = repositoryQueries.flatMap((query, index) => query.data?.repositories.map(({ repository }) => ({
    organizationId: repository.organization_id,
    repositoryId: repository.id,
    label: `${context.data?.organizations[index]?.name ?? "organization"}/${repository.name}`,
  })) ?? []);
  const [secret, setSecret] = useState("");
  const { register, handleSubmit, reset, setValue, getValues, formState: { errors } } = useForm<TokenForm>({
    defaultValues: { name: "", scopes: "read:user, read:org, issues:read", repository: "" },
  });
  const client = useQueryClient();
  const inspector = useInspector();
  const refresh = () => client.invalidateQueries({ queryKey: queryKeys.pats });
  const create = useMutation({ mutationFn: (form: TokenForm) => {
    const selected = repositoryOptions.find((repository) => repository.repositoryId === form.repository);
    return api.createPAT({
      name: form.name,
      scopes: form.scopes.split(",").map((scope) => scope.trim()).filter(Boolean),
      repositories: selected ? [{ organization_id: selected.organizationId, repository_id: selected.repositoryId }] : undefined,
      expires_at: null,
    });
  }, onSuccess: (value) => { setSecret(value.secret); reset(); void refresh(); }, onError: inspector.report });
  const rotate = useMutation({ mutationFn: api.rotatePAT, onSuccess: (value) => { setSecret(value.secret); void refresh(); }, onError: inspector.report });
  const revoke = useMutation({ mutationFn: api.revokePAT, onSuccess: () => void refresh(), onError: inspector.report });
  const tokenItems = tokens.data?.tokens ?? [];
  const activeTokens = tokenItems.filter((token) => !token.revoked_at);
  const revokedTokens = tokenItems.filter((token) => Boolean(token.revoked_at));
  return <div className="page"><PageHeader eyebrow="Account / credentials" title="Personal access tokens" description="Issue API credentials are scoped, expiring, revocable, and never recoverable after creation." />
    <div className="two-column token-layout"><Panel title="Create a token" description="Choose only the capabilities and repositories this automation needs."><form className="form-grid" onSubmit={handleSubmit((form) => create.mutate(form))}>
      <Field label="Token name" error={errors.name?.message}><TextInput {...register("name", { required: "Name is required" })} /></Field>
      <Field label="Scopes" hint="Comma-separated stable scope names."><TextInput className="input mono" {...register("scopes")} /></Field>
      <Field label="Repository access" hint="Choose one repository for runners and other repository-local automation." error={errors.repository?.message}><SelectInput aria-label="Repository access" {...register("repository", { validate: (value) => !getValues("scopes").split(",").map((scope) => scope.trim()).includes("runner:delegate") || value !== "" || "Runner delegation requires exactly one repository" })}><option value="">All repositories</option>{repositoryOptions.map((repository) => <option key={`${repository.organizationId}/${repository.repositoryId}`} value={repository.repositoryId}>{repository.label}</option>)}</SelectInput></Field>
      <div className="button-row"><button className="button secondary" type="button" onClick={() => setValue("scopes", runnerScopes, { shouldValidate: true })}><ShieldCheck size={16} />Runner preset</button><button className="button primary" type="submit" disabled={create.isPending}><Plus size={16} />Create token</button></div>
    </form></Panel>
      <Panel title="Credential hygiene"><div className="editorial-note"><KeyRound size={22} /><p>Store the generated token in an origin-bound issue-spec profile. Runner parent tokens must be bound to exactly one repository; child credentials are minted just in time and revoked after each job.</p></div></Panel></div>
    <Panel title="Active tokens" description={`${activeTokens.length} usable ${activeTokens.length === 1 ? "credential" : "credentials"}`}>
      {tokens.error ? <ErrorNotice error={tokens.error} /> : null}
      {!tokens.isLoading && activeTokens.length === 0 ? <EmptyState title="No active tokens" description="Create a narrowly scoped credential when an automation is ready to connect." /> : null}
      <div className="resource-list">{activeTokens.map((token) => <article className="resource-row" key={token.id}><div><strong>{token.name}</strong><span className="mono">{token.token_prefix ?? token.prefix ?? "prefix hidden"}</span><small>{tokenBoundaryLabel(token, repositoryOptions)}</small></div><div className="scope-list">{token.scopes.map((scope) => <StatusBadge key={scope}>{scope}</StatusBadge>)}</div><div className="row-actions"><button className="icon-button" type="button" disabled={rotate.isPending || revoke.isPending} onClick={() => rotate.mutate(token.id)} aria-label={`Rotate ${token.name}`}><RefreshCw size={17} /></button><button className="icon-button danger-text" type="button" disabled={rotate.isPending || revoke.isPending} onClick={() => revoke.mutate(token.id)} aria-label={`Revoke ${token.name}`}><Trash2 size={17} /></button></div></article>)}</div>
    </Panel>
    {revokedTokens.length > 0 ? <Panel className="token-history" title="Revoked history" description={`${revokedTokens.length} retired ${revokedTokens.length === 1 ? "credential" : "credentials"} retained for audit`}>
      <div className="resource-list">{revokedTokens.map((token) => <article className="resource-row token-revoked" key={token.id}><div><strong>{token.name}</strong><span className="mono">{token.token_prefix ?? token.prefix ?? "prefix hidden"}</span><small>{tokenBoundaryLabel(token, repositoryOptions)}</small></div><div className="scope-list">{token.scopes.map((scope) => <StatusBadge key={scope}>{scope}</StatusBadge>)}</div><div className="token-revoked-state"><StatusBadge tone="coral">Revoked</StatusBadge><time dateTime={token.revoked_at ?? undefined}>{formatDate(token.revoked_at)}</time></div></article>)}</div>
    </Panel> : null}
    {secret ? <SecretDialog secret={secret} title="Save this access token" onClose={() => setSecret("")} /> : null}
  </div>;
}

function tokenBoundaryLabel(token: PAT, repositories: RepositoryOption[]) {
  if (!token.repository_restricted) return "All repositories";
  const labels = token.repositories.map((selection) => repositories.find((repository) => repository.organizationId === selection.organization_id && repository.repositoryId === selection.repository_id)?.label).filter(Boolean);
  if (labels.length === token.repositories.length && labels.length > 0) return `Restricted to ${labels.join(", ")}`;
  return `Restricted to ${token.repositories.length} ${token.repositories.length === 1 ? "repository" : "repositories"}`;
}

function formatDate(value?: string | null) {
  if (!value) return "Revocation time unavailable";
  const date = new Date(value);
  return Number.isNaN(date.getTime()) ? value : new Intl.DateTimeFormat(undefined, { dateStyle: "medium", timeStyle: "short" }).format(date);
}
