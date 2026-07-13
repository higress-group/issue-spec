import { useMutation, useQuery, useQueryClient } from "@tanstack/react-query";
import { useForm } from "react-hook-form";
import { ArrowRight, Plus, Settings2 } from "lucide-react";
import { Link, useParams } from "react-router-dom";
import { EmptyState, ErrorNotice, Field, PageHeader, Panel, SelectInput, StatusBadge, TextInput } from "../app/components";
import { useInspector } from "../app/problem-inspector";
import { api } from "../lib/api/resources";
import { queryKeys, useCurrentContext } from "../auth/session";
import { useTranslation } from "react-i18next";

type RepoForm = { name: string; display_name: string; description: string; visibility: "public" | "internal" | "private"; default_branch: string; contribution_policy: "disabled" | "members" | "authenticated" | "public" };

export function RepositoriesPage() {
  const { t } = useTranslation();
  const { orgId = "" } = useParams();
  const repositories = useQuery({ queryKey: queryKeys.repoContext(orgId), queryFn: ({ signal }) => api.repositoriesContext(orgId, signal) });
  const context = useCurrentContext();
  const organization = context.data?.organizations.find((org) => org.id === orgId);
  const canCreate = organization?.allowed_actions.includes("organization.admin");
  const { register, handleSubmit, reset, formState: { errors } } = useForm<RepoForm>({ defaultValues: { name: "", display_name: "", description: "", visibility: "private", default_branch: "main", contribution_policy: "members" } });
  const inspector = useInspector();
  const client = useQueryClient();
  const create = useMutation({ mutationFn: (form: RepoForm) => api.createRepository(orgId, form), onSuccess: () => { reset(); void client.invalidateQueries({ queryKey: queryKeys.repoContext(orgId) }); }, onError: inspector.report });
  return <div className="page"><PageHeader eyebrow={t("repositories.eyebrow", { name: organization?.name ?? t("common.organization") })} title={t("repositories.title")} description={t("repositories.description")} actions={canCreate ? <a className="button secondary" href="#create-repository"><Plus size={16} />{t("repositories.new")}</a> : undefined} />
    {repositories.error ? <ErrorNotice error={repositories.error} /> : null}
    <div className="repo-grid">{repositories.data?.repositories.map((access) => <Link className="repo-card" to={`/orgs/${orgId}/repos/${access.repository.id}/settings`} key={access.repository.id}><div className="repo-card-top"><span className={`visibility-mark ${access.repository.visibility}`} /><StatusBadge tone={access.effective_permission === "admin" ? "purple" : "teal"}>{t(`common.permission.${access.effective_permission}`)}</StatusBadge></div><h2>{access.repository.display_name}</h2><p className="mono">{access.repository.name}</p><div className="scope-list">{access.allowed_actions.slice(0, 3).map((action) => <span key={action}>{t(`common.action.${action.replaceAll(".", "_")}`, { defaultValue: action })}</span>)}</div><div className="card-foot"><span>{t(`common.visibilityValue.${access.repository.visibility}`)}</span><ArrowRight size={18} /></div></Link>)}</div>
    {!repositories.isLoading && repositories.data?.repositories.length === 0 ? <EmptyState title={t("repositories.emptyTitle")} description={t("repositories.emptyDescription")} /> : null}
    {canCreate ? <Panel title={t("repositories.createTitle")} description={t("repositories.createDescription")} className="anchor-panel"><form id="create-repository" className="form-grid wide" onSubmit={handleSubmit((form) => create.mutate(form))}><Field label={t("common.name")} error={errors.name?.message}><TextInput {...register("name", { required: t("repositories.nameRequired") })} /></Field><Field label={t("common.displayName")} error={errors.display_name?.message}><TextInput {...register("display_name", { required: t("repositories.displayNameRequired") })} /></Field><Field label={t("common.description")}><TextInput {...register("description")} /></Field><Field label={t("common.defaultBranch")}><TextInput {...register("default_branch")} /></Field><Field label={t("common.visibility")}><SelectInput {...register("visibility")}><option value="private">{t("common.visibilityValue.private")}</option><option value="internal">{t("common.visibilityValue.internal")}</option><option value="public">{t("common.visibilityValue.public")}</option></SelectInput></Field><Field label={t("repositories.contributionPolicy")}><SelectInput {...register("contribution_policy")}><option value="disabled">{t("common.contribution.disabled")}</option><option value="members">{t("common.contribution.members")}</option><option value="authenticated">{t("common.contribution.authenticated")}</option><option value="public">{t("common.contribution.public")}</option></SelectInput></Field><button className="button primary" type="submit" disabled={create.isPending}><Settings2 size={16} />{t("repositories.create")}</button></form></Panel> : null}
  </div>;
}
