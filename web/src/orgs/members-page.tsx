import { useMemo, useState } from "react";
import { useMutation, useQuery, useQueryClient } from "@tanstack/react-query";
import { useForm } from "react-hook-form";
import { Search, Trash2, UserPlus } from "lucide-react";
import { useParams } from "react-router-dom";
import { ErrorNotice, Field, PageHeader, Panel, SelectInput, StatusBadge, TextInput } from "../app/components";
import { useInspector } from "../app/problem-inspector";
import { api } from "../lib/api/resources";
import { queryKeys } from "../auth/session";

type InviteForm = { login: string; role: string };

export function MembersPage() {
  const { orgId = "" } = useParams();
  const memberships = useQuery({ queryKey: queryKeys.memberships(orgId), queryFn: ({ signal }) => api.memberships(orgId, signal) });
  const directory = useQuery({ queryKey: ["user-candidates", orgId, "administration"], queryFn: ({ signal }) => api.userCandidates(orgId, "administration", "", "prefix", signal) });
  const names = useMemo(() => new Map(directory.data?.users.map((user) => [user.id, user])), [directory.data]);
  const [resolved, setResolved] = useState<{ id: string; login: string }>();
  const { register, handleSubmit, getValues, formState: { errors } } = useForm<InviteForm>({ defaultValues: { login: "", role: "member" } });
  const inspector = useInspector();
  const client = useQueryClient();
  const refresh = () => Promise.all([client.invalidateQueries({ queryKey: queryKeys.memberships(orgId) }), client.invalidateQueries({ queryKey: ["user-candidates", orgId] })]);
  const resolve = useMutation({ mutationFn: (login: string) => api.userCandidates(orgId, "membership", login, "exact"), onSuccess: (data) => setResolved(data.users[0] ? { id: data.users[0].id, login: data.users[0].login } : undefined), onError: inspector.report });
  const invite = useMutation({ mutationFn: (form: InviteForm) => { if (!resolved) throw new Error("Resolve a login first"); return api.inviteMembership(orgId, { user_id: resolved.id, role: form.role }); }, onSuccess: () => { setResolved(undefined); void refresh(); }, onError: inspector.report });
  const update = useMutation({ mutationFn: ({ id, role, state, version }: { id: string; role: string; state: string; version: number }) => api.updateMembership(orgId, id, { role, state, expected_version: version }), onSuccess: () => void refresh(), onError: (error, draft) => inspector.report(error, draft) });
  const remove = useMutation({ mutationFn: ({ id, version }: { id: string; version: number }) => api.deleteMembership(orgId, id, version), onSuccess: () => void refresh(), onError: inspector.report });
  return <div className="page"><PageHeader eyebrow="Organization / access" title="Members" description="Resolve immutable local logins without exposing a global user directory." />
    <Panel title="Invite by exact login" description="Prefix suggestions stay tenant-scoped; an external account is resolved only by exact login."><form className="inline-form" onSubmit={handleSubmit((form) => invite.mutate(form))}><Field label="Local login" error={errors.login?.message}><div className="input-action"><TextInput {...register("login", { required: "Login is required", onChange: () => setResolved(undefined) })} /><button className="icon-button" type="button" onClick={() => resolve.mutate(getValues("login"))} aria-label="Resolve login"><Search size={17} /></button></div></Field><Field label="Role"><SelectInput {...register("role")}><option value="reader">Reader</option><option value="member">Member</option><option value="maintainer">Maintainer</option><option value="owner">Owner</option></SelectInput></Field><div className="resolve-state">{resolved ? <StatusBadge tone="teal">Resolved @{resolved.login}</StatusBadge> : <span>Resolve before inviting</span>}</div><button className="button primary" type="submit" disabled={!resolved || invite.isPending}><UserPlus size={16} />Invite member</button></form></Panel>
    <Panel title="Current membership">{memberships.error ? <ErrorNotice error={memberships.error} /> : null}<div className="resource-list">{memberships.data?.memberships.map((membership) => { const user = names.get(membership.user_id); return <article className="resource-row" key={membership.id}><div><strong>{user?.display_name ?? "Unknown local user"}</strong><span className="mono">@{user?.login ?? membership.user_id}</span></div><StatusBadge tone={membership.state === "active" ? "teal" : "coral"}>{membership.state}</StatusBadge><SelectInput aria-label={`Role for ${user?.login ?? membership.user_id}`} value={membership.role} onChange={(event) => update.mutate({ id: membership.id, role: event.target.value, state: membership.state, version: membership.representation_version })}><option value="reader">Reader</option><option value="member">Member</option><option value="maintainer">Maintainer</option><option value="owner">Owner</option></SelectInput><button className="icon-button danger-text" type="button" onClick={() => remove.mutate({ id: membership.id, version: membership.representation_version })} aria-label={`Remove ${user?.login ?? "member"}`}><Trash2 size={17} /></button></article>; })}</div></Panel>
  </div>;
}
