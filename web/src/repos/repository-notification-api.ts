import { z } from "zod";
import { apiRequest } from "../lib/api/client";

export const repositorySubscriptionSchema = z.object({
  subscribed: z.boolean(),
  ignored: z.boolean().optional().default(false),
  reason: z.string().optional().default(""),
  representation_version: z.number().int().nonnegative().optional().default(0),
  collection_version: z.number().int().positive(),
  created_at: z.string().datetime({ offset: true }).optional(),
});

export const notificationEmailStatusSchema = z.object({
  available: z.boolean(),
  notification_email: z.string().email().nullable(),
});

const path = (orgId: string, repoId: string) =>
  `/api/v1/orgs/${encodeURIComponent(orgId)}/repos/${encodeURIComponent(repoId)}/subscription`;

export const repositoryNotificationApi = {
  subscription: (orgId: string, repoId: string, signal?: AbortSignal) =>
    apiRequest(path(orgId, repoId), { schema: repositorySubscriptionSchema, signal }),
  subscribe: (orgId: string, repoId: string) =>
    apiRequest(path(orgId, repoId), { method: "PUT", schema: repositorySubscriptionSchema }),
  unsubscribe: (orgId: string, repoId: string) =>
    apiRequest<void>(path(orgId, repoId), { method: "DELETE" }),
  emailStatus: (signal?: AbortSignal) =>
    apiRequest("/api/v1/profile/email", { schema: notificationEmailStatusSchema, signal }),
};

export const repositoryNotificationKeys = {
  subscription: (orgId: string, repoId: string) => ["repository-email-subscription", orgId, repoId] as const,
  emailStatus: ["profile-notification-email-status"] as const,
};
