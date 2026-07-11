import { apiRequest } from "../../lib/api/client";
import { contextSchema, repositoriesContextSchema } from "../../lib/api/types";
import { boardPageSchema, changeCardSchema, type BoardFilters } from "./types";

function safeSegment(value: string) {
  return encodeURIComponent(value.trim());
}

function query(filters: BoardFilters) {
  const values = new URLSearchParams({ page: String(filters.page), per_page: String(filters.perPage) });
  if (filters.stage) values.set("stage", filters.stage);
  if (filters.lifecycle) values.set("lifecycle", filters.lifecycle);
  if (filters.anomaly) values.set("anomaly", filters.anomaly);
  return values.toString();
}

export const boardApi = {
  context: (signal?: AbortSignal) => apiRequest("/api/v1/context", { schema: contextSchema, signal }),
  repositories: (orgId: string, signal?: AbortSignal) => apiRequest(`/api/v1/context/orgs/${safeSegment(orgId)}/repos`, { schema: repositoriesContextSchema, signal }),
  organizationBoard: (orgId: string, filters: BoardFilters, signal?: AbortSignal) =>
    apiRequest(`/api/v1/orgs/${safeSegment(orgId)}/changes?${query(filters)}`, { schema: boardPageSchema, signal }),
  repositoryBoard: (orgId: string, repoId: string, filters: BoardFilters, signal?: AbortSignal) =>
    apiRequest(`/api/v1/orgs/${safeSegment(orgId)}/repos/${safeSegment(repoId)}/changes?${query(filters)}`, { schema: boardPageSchema, signal }),
  change: (orgId: string, repoId: string, changeKey: string, signal?: AbortSignal) =>
    apiRequest(`/api/v1/orgs/${safeSegment(orgId)}/repos/${safeSegment(repoId)}/changes/${safeSegment(changeKey)}`, { schema: changeCardSchema, signal }),
};
