import { useMutation, useQuery, useQueryClient } from "@tanstack/react-query";
import { useForm } from "react-hook-form";
import { Bot, KeyRound, Save, Users, Workflow } from "lucide-react";
import { Link, useParams } from "react-router-dom";
import { ErrorNotice, Field, Loading, PageHeader, Panel, SelectInput, TextInput } from "../app/components";
import { useInspector } from "../app/problem-inspector";
import { api } from "../lib/api/resources";
import type { AdminOrganization, BasePermission } from "../lib/api/types";
import { queryKeys } from "../auth/session";

type OrgSettings = { display_name: string; description: string; base_permission: BasePermission };

export function OrganizationPage() {
  const { orgId = "" } = useParams();
  const organization = useQuery({ queryKey: queryKeys.organization(orgId), queryFn: ({ signal }) => api.organization(orgId, signal) });
  if (organization.isLoading) return <Loading label="Loading organization" />;
  if (organization.error) return <ErrorNotice error={organization.error} />;
  return <div className="page"><PageHeader eyebrow={`Administration / ${organization.data?.name}`} title={organization.data?.display_name ?? "Organization"} description={organization.data?.description || "No description has been written yet."} actions={<div className="button-row"><Link className="button secondary" to={`/admin/orgs/${orgId}/members`}><Users size={16} />Members</Link><Link className="button secondary" to={`/admin/orgs/${orgId}/service-accounts`}><Bot size={16} />Service accounts</Link><Link className="button secondary" to={`/admin/orgs/${orgId}/managed-tokens`}><KeyRound size={16} />Managed PATs</Link><Link className="button secondary" to={`/orgs/${orgId}/repos`}><Workflow size={16} />Repositories</Link></div>} />{organization.data ? <OrganizationSettings organization={organization.data} /> : null}</div>;
}

function OrganizationSettings({ organization }: { organization: AdminOrganization }) {
  const inspector = useInspector();
  const client = useQueryClient();
  const { register, handleSubmit } = useForm<OrgSettings>({ values: { display_name: organization.display_name, description: organization.description, base_permission: organization.base_permission } });
  const update = useMutation({ mutationFn: (form: OrgSettings) => api.updateOrganization(organization.id, { ...form, expected_version: organization.representation_version }), onSuccess: () => { inspector.note("Organization settings saved."); void client.invalidateQueries({ queryKey: queryKeys.organization(organization.id) }); }, onError: (error, draft) => inspector.report(error, draft) });
  return <Panel title="Organization settings" description={`Representation v${organization.representation_version}`}><form className="form-grid wide" onSubmit={handleSubmit((form) => update.mutate(form))}><Field label="Display name"><TextInput {...register("display_name", { required: true })} /></Field><Field label="Description"><TextInput {...register("description")} /></Field><Field label="Base repository permission"><SelectInput {...register("base_permission")}><option value="none">None</option><option value="read">Read</option><option value="triage">Triage</option><option value="write">Write</option><option value="maintain">Maintain</option><option value="admin">Admin</option></SelectInput></Field><button className="button primary" type="submit" disabled={update.isPending}><Save size={16} />Save settings</button></form></Panel>;
}
