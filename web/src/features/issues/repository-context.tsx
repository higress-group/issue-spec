import { useQuery } from "@tanstack/react-query";
import { Link, Navigate, useLocation, useParams } from "react-router-dom";
import type { ReactNode } from "react";
import { useCurrentContext } from "../../auth/session";
import { api } from "../../lib/api/resources";
import { isApiProblem } from "../../lib/api/client";
import type { OrganizationContext, RepositoryContext } from "../../lib/api/types";

export type ActiveRepository = { organization: OrganizationContext; repository: RepositoryContext; authenticated: boolean };

function segment(value: string) { return encodeURIComponent(value); }

export function repositoryIssuePath(active: ActiveRepository, number?: number) {
  const base = `/${segment(active.organization.name)}/${segment(active.repository.repository.name)}/issues`;
  return number === undefined ? base : `${base}/${number}`;
}

export function repositoryChangePath(active: ActiveRepository, change?: string) {
  const base = `/${segment(active.organization.name)}/${segment(active.repository.repository.name)}/changes`;
  return change === undefined ? base : `${base}/${segment(change)}`;
}

export function RepositoryGate({ children }: { children: (active: ActiveRepository) => ReactNode }) {
  const { orgId = "", repoId = "", owner = "", repo = "" } = useParams();
  const canonical = Boolean(owner && repo);
  const context = useCurrentContext(!canonical);
  const organization = context.data?.organizations.find((item) => item.id === orgId);
  const canonicalContext = useQuery({
    queryKey: ["context", "repository", owner.toLowerCase(), repo.toLowerCase()],
    queryFn: ({ signal }) => api.repositoryRouteContext(owner, repo, signal),
    enabled: canonical,
    retry: false,
  });
  const repositories = useQuery({
    queryKey: ["context", "repositories", orgId],
    queryFn: ({ signal }) => api.repositoriesContext(orgId, signal),
    enabled: !canonical && Boolean(organization),
  });
  if (canonical) {
    if (canonicalContext.isLoading) return <IssueLoading label="Opening repository desk" />;
    if (canonicalContext.error || !canonicalContext.data) {
      const status = isApiProblem(canonicalContext.error) ? canonicalContext.error.problem.status : 404;
      return <IssueStatus status={status === 401 ? 401 : 404} />;
    }
    return <>{children({ ...canonicalContext.data })}</>;
  }
  if (context.isLoading || repositories.isLoading) return <IssueLoading label="Opening repository desk" />;
  if (!organization || repositories.error) return <IssueStatus status={repositories.error ? 403 : 404} />;
  const repository = repositories.data?.repositories.find((item) => item.repository.id === repoId);
  if (!repository) return <IssueStatus status={404} />;
  return <>{children({ organization, repository, authenticated: true })}</>;
}

export function IssueLoading({ label = "Loading issues" }: { label?: string }) {
  return <div className="issue-status" role="status"><span className="issue-loader" aria-hidden="true" /><p>{label}…</p></div>;
}

export function IssueStatus({ status, message }: { status: number; message?: string }) {
  const location = useLocation();
  const unauthenticated = status === 401;
  const forbidden = status === 403;
  return <div className="issue-status"><span className="issue-kicker coral">{status} / {unauthenticated ? "sign in" : forbidden ? "restricted" : "not found"}</span><h1>{unauthenticated ? "Sign in to continue" : forbidden ? "This desk is outside your authority" : "That issue desk is not here"}</h1><p>{message ?? (unauthenticated ? "The presented credential was not accepted. Sign in again before retrying this repository." : forbidden ? "Your current credential cannot read or change this repository." : "It may have moved, or its existence is intentionally concealed.")}</p><Link className="issue-button primary" to={unauthenticated ? "/login" : "/issues"} state={unauthenticated ? { returnTo: `${location.pathname}${location.search}${location.hash}` } : undefined}>{unauthenticated ? "Sign in" : "Choose another repository"}</Link></div>;
}

export function MutationProblem({ error }: { error: unknown }) {
  if (!error) return null;
  const status = typeof error === "object" && error && "status" in error ? Number(error.status) : 0;
  if (status === 409) return <div className="issue-conflict" role="alert"><strong>Someone changed this while you were writing.</strong><span>Your draft is still here. Copy it or reload the latest version before trying again.</span><button type="button" onClick={() => window.location.reload()}>Reload latest</button></div>;
  if (status === 403) return <div className="issue-conflict" role="alert"><strong>This action is restricted.</strong><span>Your draft remains in the editor.</span></div>;
  return <div className="issue-conflict" role="alert"><strong>The change was not saved.</strong><span>Your draft remains in the editor so you can retry.</span></div>;
}

export function NavigateToIssue({ active, number }: { active: ActiveRepository; number: number }) {
  return <Navigate replace to={repositoryIssuePath(active, number)} />;
}
