import { useMutation, useQueryClient } from "@tanstack/react-query";
import { useForm } from "react-hook-form";
import { ArrowRight, Building2, Plus } from "lucide-react";
import { Link } from "react-router-dom";
import { EmptyState, ErrorNotice, Field, PageHeader, Panel, SelectInput, StatusBadge, TextInput } from "../app/components";
import { useInspector } from "../app/problem-inspector";
import { api } from "../lib/api/resources";
import type { BasePermission } from "../lib/api/types";
import { queryKeys, useCurrentContext } from "../auth/session";

type OrgForm = { name: string; display_name: string; description: string; base_permission: BasePermission };

export function DashboardPage() {
  const context = useCurrentContext();
  return <div className="page"><PageHeader eyebrow="Control room" title="Good work starts with orientation" description="Choose a tenant, inspect your authority, and continue the workflow from one calm desk." />
    {context.error ? <ErrorNotice error={context.error} /> : null}
    <section className="metric-strip"><div><span>Organizations</span><strong>{context.data?.organizations.length ?? 0}</strong></div><div><span>Credential realm</span><strong>{context.data?.credential.kind ?? "—"}</strong></div><div><span>Site authority</span><strong>{context.data?.allowed_actions.includes("site.admin") ? "Administrator" : "Scoped"}</strong></div></section>
    <Panel title="Your organizations" description="Only directly readable organizations or containers with visible repositories appear here.">
      {context.data?.organizations.length === 0 ? <EmptyState title="No visible workspace yet" description="Ask an organization owner to add your account, or claim bootstrap if this is a new server." /> : <div className="card-grid">{context.data?.organizations.map((org, index) => <Link className="org-card" to={`/orgs/${org.id}/repos`} key={org.id}><span className={`stage-number stage-${index % 3}`}>0{index + 1}</span><div><span className="eyebrow">{org.container_only ? "Repository container" : "Organization"}</span><h2>{org.display_name}</h2><p className="mono">{org.name}</p></div><div className="card-foot"><StatusBadge tone={org.effective_permission === "admin" ? "purple" : "teal"}>{org.effective_permission}</StatusBadge><ArrowRight size={18} /></div></Link>)}</div>}
    </Panel>
  </div>;
}

export function AdminPage() {
  const context = useCurrentContext();
  const inspector = useInspector();
  const client = useQueryClient();
  const { register, handleSubmit, reset, formState: { errors } } = useForm<OrgForm>({ defaultValues: { base_permission: "read", name: "", display_name: "", description: "" } });
  const create = useMutation({ mutationFn: api.createOrganization, onSuccess: () => { reset(); void client.invalidateQueries({ queryKey: queryKeys.context }); }, onError: inspector.report });
  const canCreate = context.data?.allowed_actions.includes("site.admin");
  return <div className="page"><PageHeader eyebrow="Administration" title="Tenant administration" description="Membership, visibility and credentials stay explicit and versioned." />
    <div className="two-column"><Panel title="Organization directory"><div className="resource-list">{context.data?.organizations.map((org) => <Link className="resource-row linked" to={`/admin/orgs/${org.id}`} key={org.id}><Building2 size={19} /><div><strong>{org.display_name}</strong><span className="mono">{org.name}</span></div><StatusBadge>{org.effective_permission}</StatusBadge><ArrowRight size={17} /></Link>)}</div></Panel>
      {canCreate ? <Panel title="Create an organization" description="Initial repositories remain organization-owned."><form className="form-grid" onSubmit={handleSubmit((body) => create.mutate(body))}><Field label="Name" error={errors.name?.message}><TextInput {...register("name", { required: "Name is required" })} /></Field><Field label="Display name" error={errors.display_name?.message}><TextInput {...register("display_name", { required: "Display name is required" })} /></Field><Field label="Description"><TextInput {...register("description")} /></Field><Field label="Base permission"><SelectInput {...register("base_permission")}><option value="none">None</option><option value="read">Read</option><option value="triage">Triage</option><option value="write">Write</option></SelectInput></Field><button className="button primary" type="submit" disabled={create.isPending}><Plus size={16} />Create organization</button></form></Panel> : <Panel title="Scoped administrator"><p>Your current credential cannot create tenants. Organization-level controls remain available where you are an owner.</p></Panel>}
    </div>
  </div>;
}
