import { useQuery } from "@tanstack/react-query";
import { Link, Navigate, useParams } from "react-router-dom";
import type { ReactNode } from "react";
import { useCurrentContext } from "../../auth/session";
import { api } from "../../lib/api/resources";
import type { OrganizationContext, RepositoryContext } from "../../lib/api/types";

export type ActiveRepository = { organization: OrganizationContext; repository: RepositoryContext };

export function RepositoryGate({ children }: { children: (active: ActiveRepository) => ReactNode }) {
  const { orgId = "", repoId = "" } = useParams();
  const context = useCurrentContext();
  const organization = context.data?.organizations.find((item) => item.id === orgId);
  const repositories = useQuery({
    queryKey: ["context", "repositories", orgId],
    queryFn: ({ signal }) => api.repositoriesContext(orgId, signal),
    enabled: Boolean(organization),
  });
  if (context.isLoading || repositories.isLoading) return <IssueLoading label="Opening repository desk" />;
  if (!organization || repositories.error) return <IssueStatus status={repositories.error ? 403 : 404} />;
  const repository = repositories.data?.repositories.find((item) => item.repository.id === repoId);
  if (!repository) return <IssueStatus status={404} />;
  return <>{children({ organization, repository })}</>;
}

export function IssueLoading({ label = "Loading issues" }: { label?: string }) {
  return <div className="issue-status" role="status"><span className="issue-loader" aria-hidden="true" /><p>{label}…</p></div>;
}

export function IssueStatus({ status, message }: { status: number; message?: string }) {
  const forbidden = status === 403;
  return <div className="issue-status"><span className="issue-kicker coral">{status} / {forbidden ? "restricted" : "not found"}</span><h1>{forbidden ? "This desk is outside your authority" : "That issue desk is not here"}</h1><p>{message ?? (forbidden ? "Your current credential cannot read or change this repository." : "It may have moved, or its existence is intentionally concealed.")}</p><Link className="issue-button primary" to="/issues">Choose another repository</Link></div>;
}

export function MutationProblem({ error }: { error: unknown }) {
  if (!error) return null;
  const status = typeof error === "object" && error && "status" in error ? Number(error.status) : 0;
  if (status === 409) return <div className="issue-conflict" role="alert"><strong>Someone changed this while you were writing.</strong><span>Your draft is still here. Copy it or reload the latest version before trying again.</span><button type="button" onClick={() => window.location.reload()}>Reload latest</button></div>;
  if (status === 403) return <div className="issue-conflict" role="alert"><strong>This action is restricted.</strong><span>Your draft remains in the editor.</span></div>;
  return <div className="issue-conflict" role="alert"><strong>The change was not saved.</strong><span>Your draft remains in the editor so you can retry.</span></div>;
}

export function NavigateToIssue({ orgId, repoId, number }: { orgId: string; repoId: string; number: number }) {
  return <Navigate replace to={`/issues/${orgId}/${repoId}/${number}`} />;
}
