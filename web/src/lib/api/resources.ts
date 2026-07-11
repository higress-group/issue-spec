import { z } from "zod";
import { apiRequest } from "./client";
import {
  bootstrapSchema,
  contextSchema,
  createdSecretSchema,
  metaSchema,
  patsSchema,
  providersSchema,
  repositoriesContextSchema,
  type AdminOrganization,
  type AdminRepository,
  type Collaborator,
  type Membership,
  type ServiceAccount,
  type UserCandidate,
} from "./types";

export const api = {
  meta: (signal?: AbortSignal) => apiRequest("/api/v1/meta", { schema: metaSchema, signal }),
  context: (signal?: AbortSignal) => apiRequest("/api/v1/context", { schema: contextSchema, signal }),
  repositoriesContext: (orgId: string, signal?: AbortSignal) => apiRequest(`/api/v1/context/orgs/${orgId}/repos`, { schema: repositoriesContextSchema, signal }),
  providers: (signal?: AbortSignal) => apiRequest("/api/v1/auth/providers", { schema: providersSchema, signal }),
  rotateSession: () => apiRequest<{ csrf_token: string }>("/api/v1/session/rotate", { method: "POST" }),
  logout: () => apiRequest<void>("/api/v1/session", { method: "DELETE" }),
  bootstrap: (signal?: AbortSignal) => apiRequest("/api/v1/bootstrap", { schema: bootstrapSchema, signal }),
  claimBootstrap: (body: { secret: string; login: string; display_name: string; email?: string }) =>
    apiRequest<{ recovery: { token: string; expires_at: string } }>("/api/v1/bootstrap/claim", { method: "POST", body }),
  exchangeRecovery: (token: string) => apiRequest<void>("/api/v1/session/recovery", { method: "POST", body: { token } }),
  pats: (signal?: AbortSignal) => apiRequest("/api/v1/pats", { schema: patsSchema, signal }),
  createPAT: (body: unknown) => apiRequest("/api/v1/pats", { method: "POST", body, schema: createdSecretSchema }),
  rotatePAT: (id: string) => apiRequest(`/api/v1/pats/${id}/rotate`, { method: "POST", schema: createdSecretSchema }),
  revokePAT: (id: string) => apiRequest<void>(`/api/v1/pats/${id}`, { method: "DELETE" }),
  organizations: (signal?: AbortSignal) => apiRequest<{ organizations: AdminOrganization[] }>("/api/v1/orgs", { signal }),
  organization: (id: string, signal?: AbortSignal) => apiRequest<AdminOrganization>(`/api/v1/orgs/${id}`, { signal }),
  createOrganization: (body: unknown) => apiRequest<AdminOrganization>("/api/v1/orgs", { method: "POST", body }),
  updateOrganization: (id: string, body: unknown) => apiRequest<AdminOrganization>(`/api/v1/orgs/${id}`, { method: "PATCH", body }),
  memberships: (orgId: string, signal?: AbortSignal) => apiRequest<{ memberships: Membership[] }>(`/api/v1/orgs/${orgId}/memberships`, { signal }),
  inviteMembership: (orgId: string, body: unknown) => apiRequest<Membership>(`/api/v1/orgs/${orgId}/memberships`, { method: "POST", body }),
  updateMembership: (orgId: string, id: string, body: unknown) => apiRequest<Membership>(`/api/v1/orgs/${orgId}/memberships/${id}`, { method: "PATCH", body }),
  deleteMembership: (orgId: string, id: string, version: number) => apiRequest<void>(`/api/v1/orgs/${orgId}/memberships/${id}?version=${version}`, { method: "DELETE" }),
  repositories: (orgId: string, signal?: AbortSignal) => apiRequest<{ repositories: AdminRepository[] }>(`/api/v1/orgs/${orgId}/repos`, { signal }),
  repository: (orgId: string, repoId: string, signal?: AbortSignal) => apiRequest<AdminRepository>(`/api/v1/orgs/${orgId}/repos/${repoId}`, { signal }),
  createRepository: (orgId: string, body: unknown) => apiRequest<AdminRepository>(`/api/v1/orgs/${orgId}/repos`, { method: "POST", body }),
  updateRepository: (orgId: string, repoId: string, body: unknown) => apiRequest<AdminRepository>(`/api/v1/orgs/${orgId}/repos/${repoId}`, { method: "PATCH", body }),
  collaborators: (orgId: string, repoId: string, signal?: AbortSignal) => apiRequest<{ collaborators: Collaborator[] }>(`/api/v1/orgs/${orgId}/repos/${repoId}/collaborators`, { signal }),
  upsertCollaborator: (orgId: string, repoId: string, body: unknown) => apiRequest<Collaborator>(`/api/v1/orgs/${orgId}/repos/${repoId}/collaborators`, { method: "POST", body }),
  deleteCollaborator: (orgId: string, repoId: string, id: string, version: number) => apiRequest<void>(`/api/v1/orgs/${orgId}/repos/${repoId}/collaborators/${id}?version=${version}`, { method: "DELETE" }),
  serviceAccounts: (orgId: string, signal?: AbortSignal) => apiRequest<{ service_accounts: ServiceAccount[] }>(`/api/v1/orgs/${orgId}/service-accounts`, { signal }),
  createServiceAccount: (orgId: string, name: string) => apiRequest<ServiceAccount>(`/api/v1/orgs/${orgId}/service-accounts`, { method: "POST", body: { name } }),
  disableServiceAccount: (orgId: string, id: string) => apiRequest<void>(`/api/v1/orgs/${orgId}/service-accounts/${id}`, { method: "DELETE" }),
  managedPATs: (orgId: string, userId: string, signal?: AbortSignal) => apiRequest<{ tokens: unknown[] }>(`/api/v1/orgs/${orgId}/users/${userId}/pats`, { signal }),
  createManagedPAT: (orgId: string, userId: string, body: unknown) => apiRequest(`/api/v1/orgs/${orgId}/users/${userId}/pats`, { method: "POST", body, schema: createdSecretSchema }),
  rotateManagedPAT: (orgId: string, tokenId: string) => apiRequest(`/api/v1/orgs/${orgId}/pats/${tokenId}/rotate`, { method: "POST", schema: createdSecretSchema }),
  revokeManagedPAT: (orgId: string, tokenId: string) => apiRequest<void>(`/api/v1/orgs/${orgId}/pats/${tokenId}`, { method: "DELETE" }),
  userCandidates: (orgId: string, purpose: string, query = "", match = "prefix", signal?: AbortSignal) => {
    const params = new URLSearchParams({ purpose, query, match });
    return apiRequest<{ users: UserCandidate[] }>(`/api/v1/orgs/${orgId}/user-candidates?${params}`, { signal });
  },
};

export const emptyString = z.string().trim().min(1, "Required");
