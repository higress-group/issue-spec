import { z } from "zod";

export const featuresSchema = z.object({
  bootstrap: z.boolean(),
  personal_access_tokens: z.boolean(),
  organizations: z.boolean(),
  source_bindings: z.boolean(),
  webhooks: z.boolean(),
  change_boards: z.boolean(),
  runner: z.boolean(),
  recovery_exchange: z.boolean(),
  search: z.boolean().optional().default(false),
  email_notifications: z.boolean().optional().default(false),
  mention_candidates: z.boolean().optional().default(false),
  repository_email_subscriptions: z.boolean().optional().default(false),
});

export const metaSchema = z.object({
  api_version: z.literal("v1"),
  features: featuresSchema,
  server_instance_id: z.string().optional().default(""),
  api_url: z.string().url().optional(),
  native_api_url: z.string().url().optional(),
  web_url: z.string().url().optional(),
  transport: z.object({ mode: z.string(), secure: z.boolean() }).optional(),
  transport_posture: z.enum(["https", "trusted-internal-http"]).optional().default("https"),
});
export type Meta = z.infer<typeof metaSchema>;

export const userSchema = z.object({
  id: z.string().uuid(),
  login: z.string(),
  display_name: z.string(),
  email: z.string().nullable().optional(),
  avatar_url: z.string().optional(),
  site_admin: z.boolean(),
});

export const organizationContextSchema = z.object({
  id: z.string().uuid(),
  name: z.string(),
  display_name: z.string(),
  effective_permission: z.string(),
  container_only: z.boolean(),
  allowed_actions: z.array(z.string()),
});

export const contextSchema = z.object({
  user: userSchema,
  credential: z.object({
    kind: z.string(),
    scope_mode: z.enum(["identity", "token"]),
    scopes: z.array(z.string()).optional(),
    repository_restricted: z.boolean(),
    repository_count: z.number().int().nonnegative().optional(),
    absolute_expires_at: z.string().datetime({ offset: true }).optional(),
    idle_expires_at: z.string().datetime({ offset: true }).optional(),
  }),
  session: z.object({ csrf_cookie_name: z.string(), csrf_header_name: z.string() }).optional(),
  allowed_actions: z.array(z.string()),
  organizations: z.array(organizationContextSchema),
});

export type CurrentContext = z.infer<typeof contextSchema>;
export type OrganizationContext = z.infer<typeof organizationContextSchema>;

export const publicProfileSchema = z.object({
  id: z.number(),
  login: z.string(),
  display_name: z.string(),
  avatar_url: z.string().optional().default(""),
  html_url: z.string().optional().default(""),
  type: z.string().default("User"),
  site_admin: z.boolean().default(false),
});

export const profileSchema = publicProfileSchema.extend({
  identity_display_name: z.string(),
  nickname: z.string().nullable(),
  representation_version: z.number().int().positive(),
  notification_email_available: z.boolean().optional().default(false),
  onboarding_completed: z.boolean().optional().default(true),
  notification_email: z.string().email().nullable().optional().default(null),
  notification_email_verified_at: z.string().datetime({ offset: true }).nullable().optional().default(null),
  pending_notification_email: z.object({
    id: z.string().uuid(),
    email: z.string().email(),
    expires_at: z.string().datetime({ offset: true }),
    sent_at: z.string().datetime({ offset: true }).nullable().optional(),
    representation_version: z.number().int().positive(),
  }).nullable().optional().default(null),
});
export type PublicProfile = z.infer<typeof publicProfileSchema>;
export type Profile = z.infer<typeof profileSchema>;

export const emailVerificationSchema = z.object({
  id: z.string().uuid(),
  email: z.string().email(),
  expires_at: z.string().datetime({ offset: true }),
  sent_at: z.string().datetime({ offset: true }).nullable().optional(),
  representation_version: z.number().int().positive(),
});

export const confirmedEmailSchema = z.object({
  status: z.literal("confirmed"),
});

export const repositoryContextSchema = z.object({
  repository: z.object({
    id: z.string().uuid(),
    organization_id: z.string().uuid(),
    name: z.string(),
    display_name: z.string(),
    visibility: z.enum(["public", "internal", "private"]),
    contribution_policy: z.enum(["disabled", "members", "authenticated", "public"]),
  }),
  effective_permission: z.string(),
  allowed_actions: z.array(z.string()),
});

export const repositoriesContextSchema = z.object({ repositories: z.array(repositoryContextSchema) });
export type RepositoryContext = z.infer<typeof repositoryContextSchema>;

export const repositoryRouteContextSchema = z.object({
  organization: organizationContextSchema,
  repository: repositoryContextSchema,
  authenticated: z.boolean(),
});
export type RepositoryRouteContext = z.infer<typeof repositoryRouteContextSchema>;

export const providersSchema = z.object({ providers: z.array(z.object({ name: z.string(), kind: z.string() })) });
export type Provider = z.infer<typeof providersSchema>["providers"][number];

export const bootstrapSchema = z.object({
  available: z.boolean(),
  completed: z.boolean(),
  completed_by_user_id: z.string().uuid().optional(),
  completed_at: z.string().datetime({ offset: true }).optional(),
  representation_version: z.number(),
});
export type BootstrapStatus = z.infer<typeof bootstrapSchema>;

export const patSchema = z.object({
  id: z.string().uuid(),
  name: z.string(),
  token_prefix: z.string().optional(),
  prefix: z.string().optional(),
  scopes: z.array(z.string()).default([]),
  repositories: z.array(z.object({
    organization_id: z.string().uuid(),
    repository_id: z.string().uuid(),
  })).default([]),
  repository_restricted: z.boolean().default(false),
  expires_at: z.string().datetime({ offset: true }).nullable().optional(),
  revoked_at: z.string().datetime({ offset: true }).nullable().optional(),
  representation_version: z.number().optional(),
});
export const patsSchema = z.object({ tokens: z.array(patSchema) });
export type PAT = z.infer<typeof patSchema>;

export const createdSecretSchema = z.object({
  plaintext: z.string().optional(),
  token: z.string().optional(),
}).passthrough().transform((value) => ({ ...value, secret: value.plaintext ?? value.token ?? "" }));

export type Visibility = "public" | "internal" | "private";
export type BasePermission = "none" | "read" | "triage" | "write" | "maintain" | "admin";

export type AdminOrganization = {
  id: string;
  name: string;
  display_name: string;
  description: string;
  base_permission: BasePermission;
  representation_version: number;
};

export type AdminRepository = {
  id: string;
  organization_id: string;
  name: string;
  display_name: string;
  description: string;
  visibility: Visibility;
  default_branch: string;
  contribution_policy: "disabled" | "members" | "authenticated" | "public";
  representation_version: number;
};

export type Membership = {
  id: string;
  organization_id: string;
  user_id: string;
  role: string;
  state: "invited" | "active" | "suspended";
  representation_version: number;
};

export type Collaborator = {
  id: string;
  user_id: string;
  role: string;
  representation_version: number;
};

export const collaboratorSchema = z.object({
  id: z.string().uuid(),
  user_id: z.string().uuid(),
  role: z.string().min(1),
  representation_version: z.number().int().positive(),
});
export const collaboratorsSchema = z.object({
  collaborators: z.array(collaboratorSchema).nullable().transform((items) => items ?? []),
});

export type ServiceAccount = {
  id: string;
  user_id: string;
  organization_id: string;
  name: string;
  login: string;
  disabled_at?: string;
  representation_version: number;
};

export type UserCandidate = {
  id: string;
  login: string;
  display_name: string;
  kind: "human" | "service_account";
  status: "active" | "disabled";
  avatar_url?: string;
  membership?: { id: string; role: string; state: string };
  service_account_id?: string;
};

const timestampSchema = z.string().datetime({ offset: true });

export const sourceBindingSchema = z.object({
  id: z.string().uuid(),
  provider_key: z.string().min(1),
  external_repository_id: z.string().min(1),
  clone_url: z.string().url(),
  web_url: z.string().url(),
  default_branch: z.string().min(1),
  version: z.number().int().positive(),
  active: z.boolean(),
  created_at: timestampSchema,
  updated_at: timestampSchema,
});
export type SourceBinding = z.infer<typeof sourceBindingSchema>;

export const webhookRetrySchema = z.object({
  max_attempts: z.number().int().min(1).max(100),
  initial_backoff: z.string().min(1),
  max_backoff: z.string().min(1),
});

export const webhookDeliveryFormatSchema = z.enum(["issue-spec.v1", "github.v3"]);
export const webhookSigningModeSchema = z.enum(["bearer", "none", "hmac-sha256"]);
export const webhookIssueActionSchema = z.enum(["opened", "edited", "closed", "reopened"]);
export const webhookCommentActionSchema = z.enum(["created", "edited"]);
export const webhookIssueKindSchema = z.enum(["ordinary", "proposal", "design", "implement"]);
export const webhookCommentClassSchema = z.enum(["human-untyped", "typed"]);
export const webhookActorClassSchema = z.enum(["human", "automation"]);
export const webhookContentPolicySchema = z.object({
  issue_actions: z.array(webhookIssueActionSchema),
  comment_actions: z.array(webhookCommentActionSchema),
  issue_kinds: z.array(webhookIssueKindSchema).min(1),
  comment_classes: z.array(webhookCommentClassSchema),
  actor_classes: z.array(webhookActorClassSchema).min(1),
});

export const webhookSubscriptionSchema = z.object({
  id: z.string().uuid(),
  organization_id: z.string().uuid(),
  repository_id: z.string().uuid().nullable().optional(),
  scope_type: z.enum(["organization", "repository"]),
  url: z.string().url(),
  active: z.boolean(),
  revoked_at: timestampSchema.nullable().optional(),
  event_types: z.array(z.string().min(1)).min(1),
  delivery_format: webhookDeliveryFormatSchema,
  signing_mode: webhookSigningModeSchema,
  content_policy: webhookContentPolicySchema,
  has_destination_query: z.boolean(),
  retry: webhookRetrySchema,
  representation_version: z.number().int().positive(),
  created_at: timestampSchema,
  updated_at: timestampSchema,
});
export const webhookSubscriptionsSchema = z.object({ subscriptions: z.array(webhookSubscriptionSchema) });
export const webhookSecretSchema = webhookSubscriptionSchema.extend({
  secret: z.string().min(1),
  secret_version: z.number().int().positive(),
});
export type WebhookSubscription = z.infer<typeof webhookSubscriptionSchema>;
export type WebhookRetry = z.infer<typeof webhookRetrySchema>;
export type WebhookSecret = z.infer<typeof webhookSecretSchema>;
export type WebhookDeliveryFormat = z.infer<typeof webhookDeliveryFormatSchema>;
export type WebhookSigningMode = z.infer<typeof webhookSigningModeSchema>;
export type WebhookContentPolicy = z.infer<typeof webhookContentPolicySchema>;

export const webhookDeliveryStateSchema = z.enum(["pending", "delivering", "succeeded", "failed", "dead"]);

export const webhookDeliverySchema = z.object({
  id: z.string().uuid(),
  scope: z.object({ OrgID: z.string().uuid(), RepoID: z.string().uuid() }),
  event_id: z.string().uuid(),
  subscription_id: z.string().uuid(),
  state: webhookDeliveryStateSchema,
  next_attempt_at: timestampSchema,
  delivered_at: timestampSchema.nullable().optional(),
  last_error: z.string().nullable().optional(),
  representation_version: z.number().int().positive(),
  created_at: timestampSchema,
  updated_at: timestampSchema,
  event_type: z.string().min(1),
  repository_sequence: z.number().int().nonnegative(),
  secret_version: z.number().int().positive(),
  delivery_format: webhookDeliveryFormatSchema,
  event_name: z.string().min(1),
  action: z.string(),
});
export const webhookDeliveriesSchema = z.object({ deliveries: z.array(webhookDeliverySchema) });
export const webhookDeliveryAttemptSchema = z.object({
  id: z.string().uuid(),
  attempt_number: z.number().int().positive(),
  response_status: z.number().int().nullable().optional(),
  response_headers: z.record(z.string(), z.array(z.string())).optional().default({}),
  error: z.string().nullable().optional(),
  started_at: timestampSchema,
  completed_at: timestampSchema.nullable().optional(),
});
export const webhookDeliveryDetailSchema = z.object({
  delivery: webhookDeliverySchema,
  attempts: z.array(webhookDeliveryAttemptSchema),
});
export type WebhookDelivery = z.infer<typeof webhookDeliverySchema>;
export type WebhookDeliveryDetail = z.infer<typeof webhookDeliveryDetailSchema>;
export type WebhookDeliveryState = z.infer<typeof webhookDeliveryStateSchema>;

export const webhookSuppressionSchema = z.object({
  id: z.string().uuid(),
  organization_id: z.string().uuid(),
  repository_id: z.string().uuid(),
  event_id: z.string().uuid(),
  subscription_id: z.string().uuid(),
  event_type: z.string().min(1),
  action: z.string().min(1),
  issue_kind: webhookIssueKindSchema,
  comment_class: webhookCommentClassSchema.nullable().optional(),
  actor_class: webhookActorClassSchema,
  reason: z.string().min(1),
  created_at: timestampSchema,
});
export const webhookSuppressionsSchema = z.object({ suppressions: z.array(webhookSuppressionSchema) });
export type WebhookSuppression = z.infer<typeof webhookSuppressionSchema>;
