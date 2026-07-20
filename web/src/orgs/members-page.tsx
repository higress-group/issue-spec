import { useMemo, useState } from "react";
import { useMutation, useQuery, useQueryClient } from "@tanstack/react-query";
import { useForm } from "react-hook-form";
import { Trash2, UserPlus } from "lucide-react";
import { useParams } from "react-router-dom";
import { ErrorNotice, Field, PageHeader, Panel, SelectInput, StatusBadge } from "../app/components";
import { useInspector } from "../app/problem-inspector";
import { api } from "../lib/api/resources";
import { queryKeys } from "../auth/session";
import { Avatar } from "../app/avatar";
import { UserAutocomplete } from "../app/user-autocomplete";
import type { UserCandidate } from "../lib/api/types";
import { useTranslation } from "react-i18next";

type InviteForm = { role: string };

export function MembersPage() {
  const { t } = useTranslation();
  const { orgId = "" } = useParams();
  const memberships = useQuery({ queryKey: queryKeys.memberships(orgId), queryFn: ({ signal }) => api.memberships(orgId, signal) });
  const directory = useQuery({ queryKey: ["user-candidates", orgId, "administration"], queryFn: ({ signal }) => api.userCandidates(orgId, "administration", "", "prefix", signal) });
  const names = useMemo(() => new Map(directory.data?.users.map((user) => [user.id, user])), [directory.data]);
  const [login, setLogin] = useState("");
  const [resolved, setResolved] = useState<UserCandidate>();
  const { register, handleSubmit } = useForm<InviteForm>({ defaultValues: { role: "member" } });
  const inspector = useInspector();
  const client = useQueryClient();
  const refresh = () => Promise.all([client.invalidateQueries({ queryKey: queryKeys.memberships(orgId) }), client.invalidateQueries({ queryKey: ["user-candidates", orgId] })]);
  const invite = useMutation({ mutationFn: (form: InviteForm) => { if (!resolved) throw new Error(t("members.resolveFirstError")); return api.inviteMembership(orgId, { user_id: resolved.id, role: form.role }); }, onSuccess: () => { setLogin(""); setResolved(undefined); void refresh(); }, onError: inspector.report });
  const update = useMutation({ mutationFn: ({ id, role, state, version }: { id: string; role: string; state: string; version: number }) => api.updateMembership(orgId, id, { role, state, expected_version: version }), onSuccess: () => void refresh(), onError: (error, draft) => inspector.report(error, draft) });
  const remove = useMutation({ mutationFn: ({ id, version }: { id: string; version: number }) => api.deleteMembership(orgId, id, version), onSuccess: () => void refresh(), onError: inspector.report });
  return <div className="page"><PageHeader eyebrow={t("members.eyebrow")} title={t("members.title")} description={t("members.description")} />
    <Panel title={t("members.inviteTitle")} description={t("members.inviteDescription")}><form className="inline-form" onSubmit={handleSubmit((form) => invite.mutate(form))}><Field label={t("common.localLogin")}><UserAutocomplete orgId={orgId} purpose="membership" label={t("common.localLogin")} value={login} onChange={(value) => { setLogin(value); setResolved(undefined); }} onSelect={setResolved} /></Field><Field label={t("common.role")}><SelectInput {...register("role")}><option value="reader">{t("common.permission.reader")}</option><option value="member">{t("common.permission.member")}</option><option value="maintainer">{t("common.permission.maintainer")}</option><option value="owner">{t("common.permission.owner")}</option></SelectInput></Field><div className="resolve-state">{resolved ? <StatusBadge tone="teal">{t("common.resolvedUser", { name: resolved.display_name, login: resolved.login })}</StatusBadge> : <span>{t("members.resolveBeforeInvite")}</span>}</div><button className="button primary" type="submit" disabled={!resolved || invite.isPending}><UserPlus size={16} />{t("members.invite")}</button></form></Panel>
    <Panel title={t("members.current")}>{memberships.error ? <ErrorNotice error={memberships.error} /> : null}<div className="resource-list">{memberships.data?.memberships.map((membership) => { const user = names.get(membership.user_id); const login = user?.login ?? membership.user_id; return <article className="resource-row user-resource-row membership-resource-row" key={membership.id}><Avatar login={user?.login ?? "unknown"} displayName={user?.display_name} src={user?.avatar_url} size={38} /><div><strong>{user?.display_name ?? t("common.unknownUser")}</strong><span className="mono">@{login}</span></div><StatusBadge tone={membership.state === "active" ? "teal" : "coral"}>{t(`common.${membership.state}`)}</StatusBadge><SelectInput aria-label={t("members.roleFor", { login })} value={membership.role} onChange={(event) => update.mutate({ id: membership.id, role: event.target.value, state: membership.state, version: membership.representation_version })}><option value="reader">{t("common.permission.reader")}</option><option value="member">{t("common.permission.member")}</option><option value="maintainer">{t("common.permission.maintainer")}</option><option value="owner">{t("common.permission.owner")}</option></SelectInput><button className="icon-button danger-text" type="button" onClick={() => remove.mutate({ id: membership.id, version: membership.representation_version })} aria-label={t("members.remove", { login: user?.login ?? t("common.permission.member") })}><Trash2 size={17} /></button></article>; })}</div></Panel>
  </div>;
}
