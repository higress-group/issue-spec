import { useMutation, useQueryClient } from "@tanstack/react-query";
import { useForm } from "react-hook-form";
import { ArrowRight, Building2, LockKeyhole, Plus, ShieldAlert } from "lucide-react";
import { Link } from "react-router-dom";
import { EmptyState, ErrorNotice, Field, PageHeader, Panel, SelectInput, StatusBadge, TextInput } from "../app/components";
import { useInspector } from "../app/problem-inspector";
import { api } from "../lib/api/resources";
import type { BasePermission } from "../lib/api/types";
import { queryKeys, useCurrentContext, useMeta } from "../auth/session";
import { useTranslation } from "react-i18next";

type OrgForm = { name: string; display_name: string; description: string; base_permission: BasePermission };

export function DashboardPage() {
  const { t } = useTranslation();
  const context = useCurrentContext();
  return <div className="page"><PageHeader eyebrow={t("dashboard.eyebrow")} title={t("dashboard.title")} description={t("dashboard.description")} />
    {context.error ? <ErrorNotice error={context.error} /> : null}
    <section className="metric-strip"><div><span>{t("dashboard.organizations")}</span><strong>{context.data?.organizations.length ?? 0}</strong></div><div><span>{t("dashboard.credentialRealm")}</span><strong>{context.data?.credential.kind ?? "—"}</strong></div><div><span>{t("dashboard.siteAuthority")}</span><strong>{context.data?.allowed_actions.includes("site.admin") ? t("dashboard.administrator") : t("dashboard.scoped")}</strong></div></section>
    <Panel title={t("dashboard.yourOrganizations")} description={t("dashboard.organizationsHelp")}>
      {context.data?.organizations.length === 0 ? <EmptyState title={t("dashboard.noWorkspace")} description={t("dashboard.noWorkspaceHelp")} /> : <div className="card-grid">{context.data?.organizations.map((org, index) => <Link className="org-card" to={`/orgs/${org.id}/repos`} key={org.id}><span className={`stage-number stage-${index % 3}`}>0{index + 1}</span><div><span className="eyebrow">{org.container_only ? t("dashboard.repositoryContainer") : t("dashboard.organization")}</span><h2>{org.display_name}</h2><p className="mono">{org.name}</p></div><div className="card-foot"><StatusBadge tone={org.effective_permission === "admin" ? "purple" : "teal"}>{org.effective_permission}</StatusBadge><ArrowRight size={18} /></div></Link>)}</div>}
    </Panel>
  </div>;
}

export function AdminPage() {
  const context = useCurrentContext();
  const meta = useMeta();
  const inspector = useInspector();
  const client = useQueryClient();
  const { register, handleSubmit, reset, formState: { errors } } = useForm<OrgForm>({ defaultValues: { base_permission: "read", name: "", display_name: "", description: "" } });
  const create = useMutation({ mutationFn: api.createOrganization, onSuccess: () => { reset(); void client.invalidateQueries({ queryKey: queryKeys.context }); }, onError: inspector.report });
  const canCreate = context.data?.allowed_actions.includes("site.admin");
  const trustedHTTP = meta.data?.transport_posture === "trusted-internal-http";
  return <div className="page"><PageHeader eyebrow="Administration" title="Tenant administration" description="Membership, visibility and credentials stay explicit and versioned." />
    <section className={`transport-posture ${trustedHTTP ? "trusted-http" : "secure-https"}`} aria-labelledby="transport-posture-heading">
      {trustedHTTP ? <ShieldAlert aria-hidden="true" /> : <LockKeyhole aria-hidden="true" />}<div><span className="eyebrow">Effective transport posture</span><h2 id="transport-posture-heading">{trustedHTTP ? "Trusted internal HTTP" : "HTTPS enforced"}</h2><p>{trustedHTTP ? "Browser cookies are intentionally usable without Secure only on this operator-managed internal deployment." : "Browser session and CSRF cookies require secure transport."}</p><dl><div><dt>API</dt><dd>{meta.data?.api_url ?? "Not reported"}</dd></div><div><dt>Web</dt><dd>{meta.data?.web_url ?? "Not reported"}</dd></div></dl></div>
    </section>
    <div className="two-column"><Panel title="Organization directory"><div className="resource-list">{context.data?.organizations.map((org) => <Link className="resource-row linked" to={`/admin/orgs/${org.id}`} key={org.id}><Building2 size={19} /><div><strong>{org.display_name}</strong><span className="mono">{org.name}</span></div><StatusBadge>{org.effective_permission}</StatusBadge><ArrowRight size={17} /></Link>)}</div></Panel>
      {canCreate ? <Panel title="Create an organization" description="Initial repositories remain organization-owned."><form className="form-grid" onSubmit={handleSubmit((body) => create.mutate(body))}><Field label="Name" error={errors.name?.message}><TextInput {...register("name", { required: "Name is required" })} /></Field><Field label="Display name" error={errors.display_name?.message}><TextInput {...register("display_name", { required: "Display name is required" })} /></Field><Field label="Description"><TextInput {...register("description")} /></Field><Field label="Base permission"><SelectInput {...register("base_permission")}><option value="none">None</option><option value="read">Read</option><option value="triage">Triage</option><option value="write">Write</option></SelectInput></Field><button className="button primary" type="submit" disabled={create.isPending}><Plus size={16} />Create organization</button></form></Panel> : <Panel title="Scoped administrator"><p>Your current credential cannot create tenants. Organization-level controls remain available where you are an owner.</p></Panel>}
    </div>
  </div>;
}
