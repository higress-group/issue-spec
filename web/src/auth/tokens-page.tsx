import { useState } from "react";
import { useMutation, useQueries, useQuery, useQueryClient } from "@tanstack/react-query";
import { Controller, useForm } from "react-hook-form";
import { KeyRound, Plus, RefreshCw, ShieldCheck, Trash2 } from "lucide-react";
import { EmptyState, ErrorNotice, Field, PageHeader, Panel, SecretDialog, StatusBadge, TextInput } from "../app/components";
import { useInspector } from "../app/problem-inspector";
import { api } from "../lib/api/resources";
import type { PAT } from "../lib/api/types";
import { queryKeys, useCurrentContext } from "./session";
import { useTranslation } from "react-i18next";
import type { TFunction } from "i18next";
import { RepositoryScopeSelector, RUNNER_TOKEN_SCOPES, TOKEN_SCOPES, TokenScopeSelector } from "./token-authority-fields";

type TokenForm = { name: string; scopes: string[]; repositories: string[] | null };
type RepositoryOption = { organizationId: string; repositoryId: string; label: string };

export function TokensPage() {
  const { t, i18n } = useTranslation();
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
    label: `${context.data?.organizations[index]?.name ?? t("personalTokens.organizationFallback")}/${repository.name}`,
  })) ?? []);
  const [secret, setSecret] = useState("");
  const { register, handleSubmit, reset, setValue, control, formState: { errors } } = useForm<TokenForm>({
    defaultValues: { name: "", scopes: [...TOKEN_SCOPES], repositories: null },
  });
  const client = useQueryClient();
  const inspector = useInspector();
  const refresh = () => client.invalidateQueries({ queryKey: queryKeys.pats });
  const create = useMutation({ mutationFn: (form: TokenForm) => {
    return api.createPAT({
      name: form.name,
      scopes: form.scopes,
      repositories: form.repositories?.flatMap((repositoryId) => {
        const selected = repositoryOptions.find((repository) => repository.repositoryId === repositoryId);
        return selected ? [{ organization_id: selected.organizationId, repository_id: selected.repositoryId }] : [];
      }),
      expires_at: null,
    });
  }, onSuccess: (value) => { setSecret(value.secret); reset(); void refresh(); }, onError: inspector.report });
  const rotate = useMutation({ mutationFn: api.rotatePAT, onSuccess: (value) => { setSecret(value.secret); void refresh(); }, onError: inspector.report });
  const revoke = useMutation({ mutationFn: api.revokePAT, onSuccess: () => void refresh(), onError: inspector.report });
  const tokenItems = tokens.data?.tokens ?? [];
  const activeTokens = tokenItems.filter((token) => !token.revoked_at);
  const revokedTokens = tokenItems.filter((token) => Boolean(token.revoked_at));
  return <div className="page"><PageHeader eyebrow={t("personalTokens.eyebrow")} title={t("personalTokens.title")} description={t("personalTokens.description")} />
    <div className="two-column token-layout"><Panel title={t("personalTokens.createTitle")} description={t("personalTokens.createDescription")}><form className="form-grid" onSubmit={handleSubmit((form) => create.mutate(form))}>
      <Field label={t("personalTokens.tokenName")} error={errors.name?.message}><TextInput {...register("name", { required: t("personalTokens.nameRequired") })} /></Field>
      <Controller name="scopes" control={control} rules={{ validate: (value) => value.length > 0 || t("tokenAuthority.scopeRequired") }} render={({ field, fieldState }) => <TokenScopeSelector value={field.value} onChange={field.onChange} error={fieldState.error?.message} />} />
      <Controller name="repositories" control={control} rules={{ validate: (value) => value === null || value.length > 0 || t("tokenAuthority.repositoryRequired") }} render={({ field, fieldState }) => <RepositoryScopeSelector name="personal-token-repositories" options={repositoryOptions.map((repository) => ({ id: repository.repositoryId, label: repository.label }))} value={field.value} onChange={field.onChange} error={fieldState.error?.message} />} />
      <div className="button-row"><button className="button secondary" type="button" onClick={() => setValue("scopes", [...RUNNER_TOKEN_SCOPES], { shouldValidate: true })}><ShieldCheck size={16} />{t("personalTokens.runnerPreset")}</button><button className="button primary" type="submit" disabled={create.isPending}><Plus size={16} />{t("personalTokens.create")}</button></div>
    </form></Panel>
      <Panel title={t("personalTokens.hygieneTitle")}><div className="editorial-note"><KeyRound size={22} /><p>{t("personalTokens.hygieneDescription")}</p></div></Panel></div>
    <Panel title={t("personalTokens.activeTitle")} description={t("personalTokens.usableCredentials", { count: activeTokens.length })}>
      {tokens.error ? <ErrorNotice error={tokens.error} /> : null}
      {!tokens.isLoading && activeTokens.length === 0 ? <EmptyState title={t("personalTokens.noActiveTitle")} description={t("personalTokens.noActiveDescription")} /> : null}
      <div className="resource-list">{activeTokens.map((token) => <article className="resource-row" key={token.id}><div><strong>{token.name}</strong><span className="mono">{token.token_prefix ?? token.prefix ?? t("personalTokens.prefixHidden")}</span><small>{tokenBoundaryLabel(token, repositoryOptions, t)}</small></div><div className="scope-list">{token.scopes.map((scope) => <StatusBadge key={scope}>{scope}</StatusBadge>)}</div><div className="row-actions"><button className="icon-button" type="button" disabled={rotate.isPending || revoke.isPending} onClick={() => rotate.mutate(token.id)} aria-label={t("personalTokens.rotate", { name: token.name })}><RefreshCw size={17} /></button><button className="icon-button danger-text" type="button" disabled={rotate.isPending || revoke.isPending} onClick={() => revoke.mutate(token.id)} aria-label={t("personalTokens.revoke", { name: token.name })}><Trash2 size={17} /></button></div></article>)}</div>
    </Panel>
    {revokedTokens.length > 0 ? <Panel className="token-history" title={t("personalTokens.revokedHistory")} description={t("personalTokens.retiredCredentials", { count: revokedTokens.length })}>
      <div className="resource-list">{revokedTokens.map((token) => <article className="resource-row token-revoked" key={token.id}><div><strong>{token.name}</strong><span className="mono">{token.token_prefix ?? token.prefix ?? t("personalTokens.prefixHidden")}</span><small>{tokenBoundaryLabel(token, repositoryOptions, t)}</small></div><div className="scope-list">{token.scopes.map((scope) => <StatusBadge key={scope}>{scope}</StatusBadge>)}</div><div className="token-revoked-state"><StatusBadge tone="coral">{t("personalTokens.revoked")}</StatusBadge><time dateTime={token.revoked_at ?? undefined}>{formatDate(token.revoked_at, i18n.resolvedLanguage, t)}</time></div></article>)}</div>
    </Panel> : null}
    {secret ? <SecretDialog secret={secret} title={t("personalTokens.saveTitle")} onClose={() => setSecret("")} /> : null}
  </div>;
}

function tokenBoundaryLabel(token: PAT, repositories: RepositoryOption[], t: TFunction) {
  if (!token.repository_restricted) return t("personalTokens.allRepositories");
  const labels = token.repositories.map((selection) => repositories.find((repository) => repository.organizationId === selection.organization_id && repository.repositoryId === selection.repository_id)?.label).filter(Boolean);
  if (labels.length === token.repositories.length && labels.length > 0) return t("personalTokens.restrictedTo", { repositories: labels.join(", ") });
  return t("personalTokens.restrictedRepositories", { count: token.repositories.length });
}

function formatDate(value: string | null | undefined, language: string | undefined, t: TFunction) {
  if (!value) return t("personalTokens.revocationUnavailable");
  const date = new Date(value);
  return Number.isNaN(date.getTime()) ? value : new Intl.DateTimeFormat(language, { dateStyle: "medium", timeStyle: "short" }).format(date);
}
