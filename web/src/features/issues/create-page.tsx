import { useMutation, useQuery, useQueryClient } from "@tanstack/react-query";
import { useState } from "react";
import { Link } from "react-router-dom";
import { useTranslation } from "react-i18next";
import { issueApi } from "./api";
import { IssueEditor } from "./issue-editor";
import { NavigateToIssue, RepositoryGate, repositoryIssuePath } from "./repository-context";
import type { ActiveRepository } from "./repository-context";

function CreateIssue({ active }: { active: ActiveRepository }) {
  const { t } = useTranslation();
  const [created, setCreated] = useState<number>();
  const queryClient = useQueryClient();
  const owner = active.organization.name;
  const repo = active.repository.repository.name;
  const labels = useQuery({ queryKey: ["issues", owner, repo, "labels"], queryFn: ({ signal }) => issueApi.listLabels(owner, repo, signal) });
  const mutation = useMutation({ mutationFn: (draft: { title: string; body: string; labels: string[] }) => issueApi.createIssue(owner, repo, draft), onSuccess: async (issue) => { await queryClient.invalidateQueries({ queryKey: ["issues", owner, repo] }); setCreated(issue.number); } });
  if (created) return <NavigateToIssue active={active} number={created} />;
  return <div className="issue-page narrow"><header className="repo-masthead"><div><Link className="issue-back" to={repositoryIssuePath(active)}>← {owner} / {repo}</Link><h1>{t("issues.create.title")}</h1><p>{t("issues.create.description")}</p></div></header><IssueEditor initial={{ title: "", body: "", labels: [] }} labels={labels.data ?? []} submitLabel={t("issues.create.submit")} pending={mutation.isPending} error={mutation.error} onSubmit={(draft) => mutation.mutate(draft)} /></div>;
}

export function IssueCreatePage() { return <RepositoryGate>{(active) => <CreateIssue active={active} />}</RepositoryGate>; }
