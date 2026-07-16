import { apiRequest } from "../../lib/api/client";
import { contextSchema, repositoriesContextSchema } from "../../lib/api/types";
import { searchPageSchema, type SearchFilters } from "./types";

function segment(value: string) { return encodeURIComponent(value.trim()); }

function query(filters: SearchFilters) {
  const values = new URLSearchParams({ q: filters.query, state: filters.state, source: filters.source,
    page: String(filters.page), per_page: String(filters.perPage) });
  if (filters.stage) values.set("stage", filters.stage);
  return values.toString();
}

export const searchApi = {
  context: (signal?: AbortSignal) => apiRequest("/api/v1/context", { schema: contextSchema, signal }),
  repositories: (orgId: string, signal?: AbortSignal) =>
    apiRequest(`/api/v1/context/orgs/${segment(orgId)}/repos`, { schema: repositoriesContextSchema, signal }),
  organization: (orgId: string, filters: SearchFilters, signal?: AbortSignal) =>
    apiRequest(`/api/v1/orgs/${segment(orgId)}/search/issues?${query(filters)}`, { schema: searchPageSchema, signal }),
  repository: (orgId: string, repoId: string, filters: SearchFilters, signal?: AbortSignal) =>
    apiRequest(`/api/v1/orgs/${segment(orgId)}/repos/${segment(repoId)}/search/issues?${query(filters)}`, { schema: searchPageSchema, signal }),
};
