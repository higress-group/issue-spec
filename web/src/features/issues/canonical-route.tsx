import { useQuery } from "@tanstack/react-query";
import { Navigate, useLocation, useParams } from "react-router-dom";
import { queryKeys, useCurrentContext } from "../../auth/session";
import { api } from "../../lib/api/resources";
import { IssueLoading, IssueStatus } from "./repository-context";

export function CanonicalIssueRoutePage() {
  const { owner = "", repo = "", issueNumber = "" } = useParams();
  const location = useLocation();
  const context = useCurrentContext();
  const number = parseIssueNumber(issueNumber);
  const organizations = context.data?.organizations.filter((item) => sameNameKey(item.name, owner)) ?? [];
  const organization = organizations.length === 1 ? organizations[0] : undefined;
  const repositories = useQuery({
    queryKey: queryKeys.repoContext(organization?.id ?? "unresolved"),
    queryFn: ({ signal }) => api.repositoriesContext(organization?.id ?? "", signal),
    enabled: Boolean(organization && number),
  });

  if (context.isLoading || (organization && number && repositories.isLoading)) {
    return <IssueLoading label="Resolving canonical issue URL" />;
  }
  if (!number || context.error || !organization || repositories.error) {
    return <IssueStatus status={404} />;
  }
  const matches = repositories.data?.repositories.filter((item) => sameNameKey(item.repository.name, repo)) ?? [];
  if (matches.length !== 1) return <IssueStatus status={404} />;
  return <Navigate replace to={{ pathname: `/issues/${organization.id}/${matches[0].repository.id}/${number}`, search: location.search, hash: location.hash }} />;
}

function parseIssueNumber(value: string): number | undefined {
  if (!/^[1-9]\d*$/.test(value)) return undefined;
  const number = Number(value);
  return Number.isSafeInteger(number) ? number : undefined;
}

function sameNameKey(left: string, right: string): boolean {
  return left.toLowerCase() === right.toLowerCase();
}
