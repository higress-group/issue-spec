import { z } from "zod";
import { apiRequest, isApiProblem } from "./client";
import {
  bootstrapSchema,
  collaboratorsSchema,
  contextSchema,
  createdSecretSchema,
  metaSchema,
  patsSchema,
  providersSchema,
  repositoriesContextSchema,
  repositoryRouteContextSchema,
  type AdminOrganization,
  type AdminRepository,
  type Collaborator,
  type Membership,
  type ServiceAccount,
  type UserCandidate,
  sourceBindingSchema,
  webhookDeliveriesSchema,
  webhookDeliveryDetailSchema,
  webhookDeliverySchema,
  webhookSecretSchema,
  webhookSubscriptionSchema,
  webhookSubscriptionsSchema,
  webhookSuppressionsSchema,
  type SourceBinding,
  type WebhookContentPolicy,
  type WebhookDeliveryFormat,
  type WebhookRetry,
  type WebhookSigningMode,
} from "./types";

type SourceBindingInput = Pick<SourceBinding, "provider_key" | "external_repository_id" | "clone_url" | "web_url" | "default_branch">;
type WebhookInput = { repository_id: string; url: string; event_types: string[]; delivery_format: WebhookDeliveryFormat; signing_mode: WebhookSigningMode; content_policy: WebhookContentPolicy; retry: WebhookRetry };
type WebhookUpdateInput = Omit<WebhookInput, "repository_id"> & { active: boolean; expected_version: number; clear_destination_query?: boolean };

export const api = {
  meta: (signal?: AbortSignal) => apiRequest("/api/v1/meta", { schema: metaSchema, signal }),
  context: (signal?: AbortSignal) => apiRequest("/api/v1/context", { schema: contextSchema, signal }),
  repositoriesContext: (orgId: string, signal?: AbortSignal) => apiRequest(`/api/v1/context/orgs/${orgId}/repos`, { schema: repositoriesContextSchema, signal }),
  repositoryRouteContext: (owner: string, repository: string, signal?: AbortSignal) =>
    apiRequest(`/api/v1/context/repos/${encodeURIComponent(owner)}/${encodeURIComponent(repository)}`, { schema: repositoryRouteContextSchema, signal }),
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
  collaborators: (orgId: string, repoId: string, signal?: AbortSignal) => apiRequest(`/api/v1/orgs/${orgId}/repos/${repoId}/collaborators`, { schema: collaboratorsSchema, signal }),
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
  activeSourceBinding: async (orgId: string, repoId: string, signal?: AbortSignal) => {
    try {
      return await apiRequest(`/api/v1/orgs/${orgId}/repos/${repoId}/bindings/active`, { schema: sourceBindingSchema, signal });
    } catch (error) {
      if (isApiProblem(error, "not_found")) return null;
      throw error;
    }
  },
  createSourceBinding: (orgId: string, repoId: string, body: SourceBindingInput) =>
    apiRequest(`/api/v1/orgs/${orgId}/repos/${repoId}/bindings`, { method: "POST", body, schema: sourceBindingSchema }),
  deactivateSourceBinding: (orgId: string, repoId: string) =>
    apiRequest<void>(`/api/v1/orgs/${orgId}/repos/${repoId}/bindings/active`, { method: "DELETE" }),
  webhookSubscriptions: (orgId: string, repoId: string, signal?: AbortSignal) => {
    const params = new URLSearchParams({ repository_id: repoId });
    return apiRequest(`/api/v1/orgs/${orgId}/webhooks?${params}`, { schema: webhookSubscriptionsSchema, signal });
  },
  createWebhookSubscription: (orgId: string, body: WebhookInput) =>
    apiRequest(`/api/v1/orgs/${orgId}/webhooks`, { method: "POST", body, schema: webhookSecretSchema }),
  updateWebhookSubscription: (orgId: string, webhookId: string, body: WebhookUpdateInput) =>
    apiRequest(`/api/v1/orgs/${orgId}/webhooks/${webhookId}`, { method: "PATCH", body, schema: webhookSubscriptionSchema }),
  rotateWebhookSecret: (orgId: string, webhookId: string) =>
    apiRequest(`/api/v1/orgs/${orgId}/webhooks/${webhookId}/rotate-secret`, { method: "POST", schema: webhookSecretSchema }),
  revokeWebhookSubscription: (orgId: string, webhookId: string) =>
    apiRequest<void>(`/api/v1/orgs/${orgId}/webhooks/${webhookId}`, { method: "DELETE" }),
  webhookSuppressions: (orgId: string, webhookId: string, signal?: AbortSignal) =>
    apiRequest(`/api/v1/orgs/${orgId}/webhooks/${webhookId}/suppressions`, { schema: webhookSuppressionsSchema, signal }),
  webhookDeliveries: (orgId: string, repoId: string, signal?: AbortSignal) =>
    apiRequest(`/api/v1/orgs/${orgId}/repos/${repoId}/deliveries`, { schema: webhookDeliveriesSchema, signal }),
  webhookDelivery: (orgId: string, repoId: string, deliveryId: string, signal?: AbortSignal) =>
    apiRequest(`/api/v1/orgs/${orgId}/repos/${repoId}/deliveries/${deliveryId}`, { schema: webhookDeliveryDetailSchema, signal }),
  redeliverWebhookDelivery: (orgId: string, repoId: string, deliveryId: string) =>
    apiRequest(`/api/v1/orgs/${orgId}/repos/${repoId}/deliveries/${deliveryId}/redeliver`, { method: "POST", schema: webhookDeliverySchema }),
};

export const emptyString = z.string().trim().min(1, "Required");
