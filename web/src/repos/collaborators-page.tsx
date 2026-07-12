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

type CollaboratorForm = { login: string; role: string };

export function CollaboratorsPage() {
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
  const add = useMutation({ mutationFn: (form: CollaboratorForm) => { if (!resolved) throw new Error("Resolve a login first"); return api.upsertCollaborator(orgId, repoId, { user_id: resolved.id, role: form.role }); }, onSuccess: () => { setResolved(undefined); void refresh(); }, onError: inspector.report });
  const remove = useMutation({ mutationFn: ({ id, version }: { id: string; version: number }) => api.deleteCollaborator(orgId, repoId, id, version), onSuccess: () => void refresh(), onError: inspector.report });
  if (repository.isLoading) return <Loading label="Loading repository access" />;
  if (repository.error) return <ErrorNotice error={repository.error} />;
  if (!repository.data) return null;
  const items = collaborators.data ? collaborators.data.collaborators : [];
  return <div className="page"><RepositoryHeader repository={repository.data} section="collaborators" title="Collaborators" description="Repository grants raise authority explicitly; token scopes can still cap them." />
    <Panel title="Add by exact login"><form className="inline-form" onSubmit={handleSubmit((form) => add.mutate(form))}><Field label="Local login"><div className="input-action"><TextInput {...register("login", { onChange: () => setResolved(undefined) })} /><button className="icon-button" type="button" onClick={() => resolve.mutate(getValues("login"))} aria-label="Resolve collaborator"><Search size={17} /></button></div></Field><Field label="Role"><SelectInput {...register("role")}><option value="read">Read</option><option value="triage">Triage</option><option value="write">Write</option><option value="maintain">Maintain</option><option value="admin">Admin</option></SelectInput></Field><div className="resolve-state">{resolved ? <StatusBadge tone="teal">Resolved @{resolved.login}</StatusBadge> : <span>Resolve before adding</span>}</div><button className="button primary" type="submit" disabled={!resolved}><UserPlus size={16} />Add collaborator</button></form></Panel>
    <Panel title="Explicit grants">{collaborators.isLoading ? <Loading label="Loading collaborators" /> : null}{collaborators.error ? <ErrorNotice error={collaborators.error} /> : null}{!collaborators.isLoading && !collaborators.error && items.length === 0 ? <EmptyState title="No explicit collaborators" description="Access currently comes from organization membership and repository defaults." /> : null}<div className="resource-list">{items.map((collaborator) => { const user = names.get(collaborator.user_id); return <article className="resource-row user-resource-row" key={collaborator.id}><Avatar login={user?.login ?? "unknown"} displayName={user?.display_name} src={user?.avatar_url} size={38} /><div><strong>{user?.display_name ?? "Unknown local user"}</strong><span className="mono">@{user?.login ?? collaborator.user_id}</span></div><StatusBadge tone="purple">{collaborator.role}</StatusBadge><button className="icon-button danger-text" type="button" onClick={() => remove.mutate({ id: collaborator.id, version: collaborator.representation_version })} aria-label={`Remove ${user?.login ?? "collaborator"}`}><Trash2 size={17} /></button></article>; })}</div></Panel>
  </div>;
}
