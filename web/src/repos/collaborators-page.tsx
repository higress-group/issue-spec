import { useMemo, useState } from "react";
import { useMutation, useQuery, useQueryClient } from "@tanstack/react-query";
import { useForm } from "react-hook-form";
import { Search, Trash2, UserPlus } from "lucide-react";
import { EmptyState, ErrorNotice, Field, Loading, Panel, SelectInput, StatusBadge, TextInput } from "../app/components";
import { useInspector } from "../app/problem-inspector";
import { api } from "../lib/api/resources";
import { queryKeys } from "../auth/session";
import { RepositoryHeader, useRepositoryContext } from "./repository-header";
import { Avatar } from "../app/avatar";
import { useTranslation } from "react-i18next";

type CollaboratorForm = { login: string; role: string };

export function CollaboratorsPage() {
  const { t } = useTranslation();
  const { orgId, repoId, repository } = useRepositoryContext();
  const collaborators = useQuery({ queryKey: queryKeys.collaborators(orgId, repoId), queryFn: ({ signal }) => api.collaborators(orgId, repoId, signal) });
  const directory = useQuery({ queryKey: ["user-candidates", orgId, "administration"], queryFn: ({ signal }) => api.userCandidates(orgId, "administration", "", "prefix", signal) });
  const names = useMemo(() => new Map(directory.data ? directory.data.users.map((user) => [user.id, user]) : []), [directory.data]);
  const [resolved, setResolved] = useState<{ id: string; login: string }>();
  const { register, handleSubmit, getValues } = useForm<CollaboratorForm>({ defaultValues: { login: "", role: "read" } });
  const inspector = useInspector();
  const client = useQueryClient();
  const refresh = () => client.invalidateQueries({ queryKey: queryKeys.collaborators(orgId, repoId) });
  const resolve = useMutation({ mutationFn: (login: string) => api.userCandidates(orgId, "collaborator", login, "exact"), onSuccess: (data) => setResolved(data.users[0] ? { id: data.users[0].id, login: data.users[0].login } : undefined), onError: inspector.report });
  const add = useMutation({ mutationFn: (form: CollaboratorForm) => { if (!resolved) throw new Error(t("collaborators.resolveFirstError")); return api.upsertCollaborator(orgId, repoId, { user_id: resolved.id, role: form.role }); }, onSuccess: () => { setResolved(undefined); void refresh(); }, onError: inspector.report });
  const remove = useMutation({ mutationFn: ({ id, version }: { id: string; version: number }) => api.deleteCollaborator(orgId, repoId, id, version), onSuccess: () => void refresh(), onError: inspector.report });
  if (repository.isLoading) return <Loading label={t("collaborators.loadingAccess")} />;
  if (repository.error) return <ErrorNotice error={repository.error} />;
  if (!repository.data) return null;
  const items = collaborators.data ? collaborators.data.collaborators : [];
  return <div className="page"><RepositoryHeader repository={repository.data} section="collaborators" title={t("collaborators.title")} description={t("collaborators.description")} />
    <Panel title={t("collaborators.addTitle")}><form className="inline-form" onSubmit={handleSubmit((form) => add.mutate(form))}><Field label={t("common.localLogin")}><div className="input-action"><TextInput {...register("login", { onChange: () => setResolved(undefined) })} /><button className="icon-button" type="button" onClick={() => resolve.mutate(getValues("login"))} aria-label={t("collaborators.resolve")}><Search size={17} /></button></div></Field><Field label={t("common.role")}><SelectInput {...register("role")}><option value="read">{t("common.permission.read")}</option><option value="triage">{t("common.permission.triage")}</option><option value="write">{t("common.permission.write")}</option><option value="maintain">{t("common.permission.maintain")}</option><option value="admin">{t("common.permission.admin")}</option></SelectInput></Field><div className="resolve-state">{resolved ? <StatusBadge tone="teal">{t("common.resolvedLogin", { login: resolved.login })}</StatusBadge> : <span>{t("collaborators.resolveBeforeAdd")}</span>}</div><button className="button primary" type="submit" disabled={!resolved}><UserPlus size={16} />{t("collaborators.add")}</button></form></Panel>
    <Panel title={t("collaborators.grants")}>{collaborators.isLoading ? <Loading label={t("collaborators.loading")} /> : null}{collaborators.error ? <ErrorNotice error={collaborators.error} /> : null}{!collaborators.isLoading && !collaborators.error && items.length === 0 ? <EmptyState title={t("collaborators.emptyTitle")} description={t("collaborators.emptyDescription")} /> : null}<div className="resource-list">{items.map((collaborator) => { const user = names.get(collaborator.user_id); const login = user?.login ?? collaborator.user_id; return <article className="resource-row user-resource-row" key={collaborator.id}><Avatar login={user?.login ?? "unknown"} displayName={user?.display_name} src={user?.avatar_url} size={38} /><div><strong>{user?.display_name ?? t("common.unknownUser")}</strong><span className="mono">@{login}</span></div><StatusBadge tone="purple">{t(`common.permission.${collaborator.role}`)}</StatusBadge><button className="icon-button danger-text" type="button" onClick={() => remove.mutate({ id: collaborator.id, version: collaborator.representation_version })} aria-label={t("collaborators.remove", { login: user?.login ?? t("common.collaborators") })}><Trash2 size={17} /></button></article>; })}</div></Panel>
  </div>;
}
