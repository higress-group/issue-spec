import { useMutation, useQueryClient } from "@tanstack/react-query";
import { useForm } from "react-hook-form";
import { Save } from "lucide-react";
import { useParams } from "react-router-dom";
import { ErrorNotice, Field, Loading, Panel, SelectInput, TextInput } from "../app/components";
import { useInspector } from "../app/problem-inspector";
import { api } from "../lib/api/resources";
import type { AdminRepository } from "../lib/api/types";
import { queryKeys } from "../auth/session";
import { RepositoryHeader, useRepositoryContext } from "./repository-header";
import { useTranslation } from "react-i18next";

type RepoSettings = Pick<AdminRepository, "display_name" | "description" | "visibility" | "default_branch" | "contribution_policy">;

export function RepositorySettingsPage() {
  const { t } = useTranslation();
  const { repository } = useRepositoryContext();
  if (repository.isLoading) return <Loading label={t("repositorySettings.loading")} />;
  if (repository.error) return <ErrorNotice error={repository.error} />;
  if (!repository.data) return null;
  return <div className="page"><RepositoryHeader repository={repository.data} section="settings" title={repository.data.display_name} description={t("repositorySettings.description")} /><RepositoryForm repository={repository.data} /></div>;
}

function RepositoryForm({ repository }: { repository: AdminRepository }) {
  const { t } = useTranslation();
  const { orgId = "" } = useParams();
  const { register, handleSubmit } = useForm<RepoSettings>({ values: { display_name: repository.display_name, description: repository.description, visibility: repository.visibility, default_branch: repository.default_branch, contribution_policy: repository.contribution_policy } });
  const inspector = useInspector();
  const client = useQueryClient();
  const update = useMutation({ mutationFn: (form: RepoSettings) => api.updateRepository(orgId, repository.id, { ...form, expected_version: repository.representation_version }), onSuccess: () => { inspector.note(t("repositorySettings.saved")); void client.invalidateQueries({ queryKey: queryKeys.repository(orgId, repository.id) }); void client.invalidateQueries({ queryKey: queryKeys.repoContext(orgId) }); }, onError: (error, draft) => inspector.report(error, draft) });
  return <Panel title={t("repositorySettings.policy")} description={t("common.representation", { version: repository.representation_version })}><form className="form-grid wide" onSubmit={handleSubmit((form) => update.mutate(form))}><Field label={t("common.displayName")}><TextInput {...register("display_name")} /></Field><Field label={t("common.description")}><TextInput {...register("description")} /></Field><Field label={t("common.visibility")}><SelectInput {...register("visibility")}><option value="private">{t("common.visibilityValue.private")}</option><option value="internal">{t("common.visibilityValue.internal")}</option><option value="public">{t("common.visibilityValue.public")}</option></SelectInput></Field><Field label={t("repositorySettings.contributionPolicy")}><SelectInput {...register("contribution_policy")}><option value="disabled">{t("common.contribution.disabled")}</option><option value="members">{t("common.contribution.members")}</option><option value="authenticated">{t("common.contribution.authenticated")}</option><option value="public">{t("common.contribution.public")}</option></SelectInput></Field><Field label={t("common.defaultBranch")}><TextInput {...register("default_branch")} /></Field><button className="button primary" type="submit" disabled={update.isPending}><Save size={16} />{t("repositorySettings.save")}</button></form></Panel>;
}
