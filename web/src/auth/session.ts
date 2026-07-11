import { useQuery } from "@tanstack/react-query";
import { api } from "../lib/api/resources";
import { isApiProblem } from "../lib/api/client";

export const queryKeys = {
  meta: ["meta"] as const,
  context: ["context"] as const,
  providers: ["providers"] as const,
  bootstrap: ["bootstrap"] as const,
  pats: ["pats"] as const,
  repoContext: (orgId: string) => ["context", "repositories", orgId] as const,
  organization: (orgId: string) => ["organization", orgId] as const,
  memberships: (orgId: string) => ["memberships", orgId] as const,
  serviceAccounts: (orgId: string) => ["service-accounts", orgId] as const,
  repository: (orgId: string, repoId: string) => ["repository", orgId, repoId] as const,
  collaborators: (orgId: string, repoId: string) => ["collaborators", orgId, repoId] as const,
  sourceBinding: (orgId: string, repoId: string) => ["source-binding", orgId, repoId] as const,
  webhooks: (orgId: string, repoId: string) => ["webhooks", orgId, repoId] as const,
  deliveries: (orgId: string, repoId: string) => ["webhook-deliveries", orgId, repoId] as const,
  delivery: (orgId: string, repoId: string, deliveryId: string) => ["webhook-delivery", orgId, repoId, deliveryId] as const,
};

export function useMeta() {
  return useQuery({ queryKey: queryKeys.meta, queryFn: ({ signal }) => api.meta(signal), staleTime: 60_000 });
}

export function useCurrentContext() {
  return useQuery({
    queryKey: queryKeys.context,
    queryFn: ({ signal }) => api.context(signal),
    retry: (failureCount, error) => !isApiProblem(error) && failureCount < 1,
  });
}
