import { useMutation, useQuery, useQueryClient } from "@tanstack/react-query";
import { useForm } from "react-hook-form";
import { Bot, KeyRound, Save, Users, Workflow } from "lucide-react";
import { Link, useParams } from "react-router-dom";
import { ErrorNotice, Field, Loading, PageHeader, Panel, SelectInput, TextInput } from "../app/components";
import { useInspector } from "../app/problem-inspector";
import { api } from "../lib/api/resources";
import type { AdminOrganization, BasePermission } from "../lib/api/types";
import { queryKeys } from "../auth/session";
import { useTranslation } from "react-i18next";

type OrgSettings = { display_name: string; description: string; base_permission: BasePermission };

export function OrganizationPage() {
  const { t } = useTranslation();
  const { orgId = "" } = useParams();
  const organization = useQuery({ queryKey: queryKeys.organization(orgId), queryFn: ({ signal }) => api.organization(orgId, signal) });
  if (organization.isLoading) return <Loading label={t("organization.loading")} />;
  if (organization.error) return <ErrorNotice error={organization.error} />;
  return <div className="page"><PageHeader eyebrow={t("organization.eyebrow", { name: organization.data?.name })} title={organization.data?.display_name ?? t("common.organization")} description={organization.data?.description || t("common.noDescription")} actions={<div className="button-row"><Link className="button secondary" to={`/admin/orgs/${orgId}/members`}><Users size={16} />{t("common.members")}</Link><Link className="button secondary" to={`/admin/orgs/${orgId}/service-accounts`}><Bot size={16} />{t("common.serviceAccounts")}</Link><Link className="button secondary" to={`/admin/orgs/${orgId}/managed-tokens`}><KeyRound size={16} />{t("common.managedTokens")}</Link><Link className="button secondary" to={`/orgs/${orgId}/repos`}><Workflow size={16} />{t("common.repositories")}</Link></div>} />{organization.data ? <OrganizationSettings organization={organization.data} /> : null}</div>;
}

function OrganizationSettings({ organization }: { organization: AdminOrganization }) {
  const { t } = useTranslation();
  const inspector = useInspector();
  const client = useQueryClient();
  const { register, handleSubmit } = useForm<OrgSettings>({ values: { display_name: organization.display_name, description: organization.description, base_permission: organization.base_permission } });
  const update = useMutation({ mutationFn: (form: OrgSettings) => api.updateOrganization(organization.id, { ...form, expected_version: organization.representation_version }), onSuccess: () => { inspector.note(t("organization.settingsSaved")); void client.invalidateQueries({ queryKey: queryKeys.organization(organization.id) }); }, onError: (error, draft) => inspector.report(error, draft) });
  return <Panel title={t("organization.settings")} description={t("common.representation", { version: organization.representation_version })}><form className="form-grid wide" onSubmit={handleSubmit((form) => update.mutate(form))}><Field label={t("common.displayName")}><TextInput {...register("display_name", { required: true })} /></Field><Field label={t("common.description")}><TextInput {...register("description")} /></Field><Field label={t("organization.baseRepositoryPermission")}><SelectInput {...register("base_permission")}><option value="none">{t("common.permission.none")}</option><option value="read">{t("common.permission.read")}</option><option value="triage">{t("common.permission.triage")}</option><option value="write">{t("common.permission.write")}</option><option value="maintain">{t("common.permission.maintain")}</option><option value="admin">{t("common.permission.admin")}</option></SelectInput></Field><button className="button primary" type="submit" disabled={update.isPending}><Save size={16} />{t("organization.saveSettings")}</button></form></Panel>;
}
