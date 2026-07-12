import { useMutation, useQuery, useQueryClient } from "@tanstack/react-query";
import { useForm } from "react-hook-form";
import { ArrowRight, Plus, Settings2 } from "lucide-react";
import { Link, useParams } from "react-router-dom";
import { EmptyState, ErrorNotice, Field, PageHeader, Panel, SelectInput, StatusBadge, TextInput } from "../app/components";
import { useInspector } from "../app/problem-inspector";
import { api } from "../lib/api/resources";
import { queryKeys, useCurrentContext } from "../auth/session";

type RepoForm = { name: string; display_name: string; description: string; visibility: "public" | "internal" | "private"; default_branch: string; contribution_policy: "disabled" | "members" | "authenticated" | "public" };

export function RepositoriesPage() {
  const { orgId = "" } = useParams();
  const repositories = useQuery({ queryKey: queryKeys.repoContext(orgId), queryFn: ({ signal }) => api.repositoriesContext(orgId, signal) });
  const context = useCurrentContext();
  const organization = context.data?.organizations.find((org) => org.id === orgId);
  const canCreate = organization?.allowed_actions.includes("organization.admin");
  const { register, handleSubmit, reset, formState: { errors } } = useForm<RepoForm>({ defaultValues: { name: "", display_name: "", description: "", visibility: "private", default_branch: "main", contribution_policy: "members" } });
  const inspector = useInspector();
  const client = useQueryClient();
  const create = useMutation({ mutationFn: (form: RepoForm) => api.createRepository(orgId, form), onSuccess: () => { reset(); void client.invalidateQueries({ queryKey: queryKeys.repoContext(orgId) }); }, onError: inspector.report });
  return <div className="page"><PageHeader eyebrow={`Workspace / ${organization?.name ?? "organization"}`} title="Repositories" description="Issue repositories are workflow containers. Source browsing and pull-request hosting remain external." actions={canCreate ? <a className="button secondary" href="#create-repository"><Plus size={16} />New repository</a> : undefined} />
    {repositories.error ? <ErrorNotice error={repositories.error} /> : null}
    <div className="repo-grid">{repositories.data?.repositories.map((access) => <Link className="repo-card" to={`/orgs/${orgId}/repos/${access.repository.id}/settings`} key={access.repository.id}><div className="repo-card-top"><span className={`visibility-mark ${access.repository.visibility}`} /><StatusBadge tone={access.effective_permission === "admin" ? "purple" : "teal"}>{access.effective_permission}</StatusBadge></div><h2>{access.repository.display_name}</h2><p className="mono">{access.repository.name}</p><div className="scope-list">{access.allowed_actions.slice(0, 3).map((action) => <span key={action}>{action}</span>)}</div><div className="card-foot"><span>{access.repository.visibility}</span><ArrowRight size={18} /></div></Link>)}</div>
    {!repositories.isLoading && repositories.data?.repositories.length === 0 ? <EmptyState title="No visible repositories" description="This organization does not contain a repository your current identity and credential can read." /> : null}
    {canCreate ? <Panel title="Create a repository" description="A private, member-contribution repository is the safest default." className="anchor-panel"><form id="create-repository" className="form-grid wide" onSubmit={handleSubmit((form) => create.mutate(form))}><Field label="Name" error={errors.name?.message}><TextInput {...register("name", { required: "Name is required" })} /></Field><Field label="Display name" error={errors.display_name?.message}><TextInput {...register("display_name", { required: "Display name is required" })} /></Field><Field label="Description"><TextInput {...register("description")} /></Field><Field label="Default branch"><TextInput {...register("default_branch")} /></Field><Field label="Visibility"><SelectInput {...register("visibility")}><option value="private">Private</option><option value="internal">Internal</option><option value="public">Public</option></SelectInput></Field><Field label="Contribution policy"><SelectInput {...register("contribution_policy")}><option value="disabled">Disabled</option><option value="members">Members</option><option value="authenticated">Authenticated users</option><option value="public">Public repository users</option></SelectInput></Field><button className="button primary" type="submit" disabled={create.isPending}><Settings2 size={16} />Create repository</button></form></Panel> : null}
  </div>;
}
