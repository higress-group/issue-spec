import { useMutation, useQuery, useQueryClient } from "@tanstack/react-query";
import { useForm } from "react-hook-form";
import { Cable, Save, Users } from "lucide-react";
import { Link, useParams } from "react-router-dom";
import { ErrorNotice, Field, Loading, PageHeader, Panel, SelectInput, TextInput } from "../app/components";
import { useInspector } from "../app/problem-inspector";
import { api } from "../lib/api/resources";
import type { AdminRepository } from "../lib/api/types";
import { queryKeys } from "../auth/session";

type RepoSettings = Pick<AdminRepository, "display_name" | "description" | "visibility" | "default_branch" | "contribution_policy">;

export function RepositorySettingsPage() {
  const { orgId = "", repoId = "" } = useParams();
  const repository = useQuery({ queryKey: queryKeys.repository(orgId, repoId), queryFn: ({ signal }) => api.repository(orgId, repoId, signal) });
  if (repository.isLoading) return <Loading label="Loading repository settings" />;
  if (repository.error) return <ErrorNotice error={repository.error} />;
  return <div className="page"><PageHeader eyebrow="Repository / settings" title={repository.data?.display_name ?? "Repository"} description="Visibility and contribution are independent, versioned controls." actions={<div className="button-row"><Link className="button secondary" to={`/orgs/${orgId}/repos/${repoId}/collaborators`}><Users size={16} />Collaborators</Link><Link className="button secondary" to={`/orgs/${orgId}/repos/${repoId}/integrations/source`}><Cable size={16} />Integrations</Link></div>} />{repository.data ? <RepositoryForm repository={repository.data} /> : null}</div>;
}

function RepositoryForm({ repository }: { repository: AdminRepository }) {
  const { orgId = "" } = useParams();
  const { register, handleSubmit } = useForm<RepoSettings>({ values: { display_name: repository.display_name, description: repository.description, visibility: repository.visibility, default_branch: repository.default_branch, contribution_policy: repository.contribution_policy } });
  const inspector = useInspector();
  const client = useQueryClient();
  const update = useMutation({ mutationFn: (form: RepoSettings) => api.updateRepository(orgId, repository.id, { ...form, expected_version: repository.representation_version }), onSuccess: () => { inspector.note("Repository settings saved."); void client.invalidateQueries({ queryKey: queryKeys.repository(orgId, repository.id) }); void client.invalidateQueries({ queryKey: queryKeys.repoContext(orgId) }); }, onError: (error, draft) => inspector.report(error, draft) });
  return <Panel title="Repository policy" description={`Representation v${repository.representation_version}`}><form className="form-grid wide" onSubmit={handleSubmit((form) => update.mutate(form))}><Field label="Display name"><TextInput {...register("display_name")} /></Field><Field label="Description"><TextInput {...register("description")} /></Field><Field label="Visibility"><SelectInput {...register("visibility")}><option value="private">Private</option><option value="internal">Internal</option><option value="public">Public</option></SelectInput></Field><Field label="Contribution policy"><SelectInput {...register("contribution_policy")}><option value="disabled">Disabled</option><option value="members">Members</option><option value="authenticated">Authenticated</option><option value="public">Public</option></SelectInput></Field><Field label="Default branch"><TextInput {...register("default_branch")} /></Field><button className="button primary" type="submit" disabled={update.isPending}><Save size={16} />Save policy</button></form></Panel>;
}
