import { useQuery } from "@tanstack/react-query";
import { Link, Navigate, useLocation, useParams } from "react-router-dom";
import type { ReactNode } from "react";
import { useTranslation } from "react-i18next";
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
  const { t } = useTranslation();
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
    if (canonicalContext.isLoading) return <IssueLoading label={t("issues.gate.openingRepository")} />;
    if (canonicalContext.error || !canonicalContext.data) {
      const status = isApiProblem(canonicalContext.error) ? canonicalContext.error.problem.status : 404;
      return <IssueStatus status={status === 401 ? 401 : 404} />;
    }
    return <>{children({ ...canonicalContext.data })}</>;
  }
  if (context.isLoading || repositories.isLoading) return <IssueLoading label={t("issues.gate.openingRepository")} />;
  if (!organization || repositories.error) return <IssueStatus status={repositories.error ? 403 : 404} />;
  const repository = repositories.data?.repositories.find((item) => item.repository.id === repoId);
  if (!repository) return <IssueStatus status={404} />;
  return <>{children({ organization, repository, authenticated: true })}</>;
}

export function IssueLoading({ label }: { label?: string }) {
  const { t } = useTranslation();
  return <div className="issue-status" role="status"><span className="issue-loader" aria-hidden="true" /><p>{label ?? t("issues.gate.loading")}…</p></div>;
}

export function IssueStatus({ status, message }: { status: number; message?: string }) {
  const { t } = useTranslation();
  const location = useLocation();
  const unauthenticated = status === 401;
  const forbidden = status === 403;
  return <div className="issue-status"><span className="issue-kicker coral">{status} / {unauthenticated ? t("issues.gate.signIn") : forbidden ? t("issues.gate.restricted") : t("issues.gate.notFound")}</span><h1>{unauthenticated ? t("issues.gate.signInTitle") : forbidden ? t("issues.gate.restrictedTitle") : t("issues.gate.notFoundTitle")}</h1><p>{message ?? (unauthenticated ? t("issues.gate.signInDescription") : forbidden ? t("issues.gate.restrictedDescription") : t("issues.gate.notFoundDescription"))}</p><Link className="issue-button primary" to={unauthenticated ? "/login" : "/issues"} state={unauthenticated ? { returnTo: `${location.pathname}${location.search}${location.hash}` } : undefined}>{unauthenticated ? t("issues.gate.signInAction") : t("issues.gate.chooseRepository")}</Link></div>;
}

export function MutationProblem({ error }: { error: unknown }) {
  const { t } = useTranslation();
  if (!error) return null;
  const status = typeof error === "object" && error && "status" in error ? Number(error.status) : 0;
  if (status === 409) return <div className="issue-conflict" role="alert"><strong>{t("issues.mutation.conflictTitle")}</strong><span>{t("issues.mutation.conflictDescription")}</span><button type="button" onClick={() => window.location.reload()}>{t("issues.mutation.reload")}</button></div>;
  if (status === 403) return <div className="issue-conflict" role="alert"><strong>{t("issues.mutation.restrictedTitle")}</strong><span>{t("issues.mutation.draftRemains")}</span></div>;
  return <div className="issue-conflict" role="alert"><strong>{t("issues.mutation.failedTitle")}</strong><span>{t("issues.mutation.failedDescription")}</span></div>;
}

export function NavigateToIssue({ active, number }: { active: ActiveRepository; number: number }) {
  return <Navigate replace to={repositoryIssuePath(active, number)} />;
}
