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
});

export const metaSchema = z.object({ api_version: z.literal("v1"), features: featuresSchema });
export type Meta = z.infer<typeof metaSchema>;

export const userSchema = z.object({
  id: z.string().uuid(),
  login: z.string(),
  display_name: z.string(),
  email: z.string().nullable().optional(),
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
    absolute_expires_at: z.string().datetime().optional(),
    idle_expires_at: z.string().datetime().optional(),
  }),
  session: z.object({ csrf_cookie_name: z.string(), csrf_header_name: z.string() }).optional(),
  allowed_actions: z.array(z.string()),
  organizations: z.array(organizationContextSchema),
});

export type CurrentContext = z.infer<typeof contextSchema>;
export type OrganizationContext = z.infer<typeof organizationContextSchema>;

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

export const providersSchema = z.object({ providers: z.array(z.object({ name: z.string(), kind: z.string() })) });
export type Provider = z.infer<typeof providersSchema>["providers"][number];

export const bootstrapSchema = z.object({
  available: z.boolean(),
  completed: z.boolean(),
  completed_by_user_id: z.string().uuid().optional(),
  completed_at: z.string().datetime().optional(),
  representation_version: z.number(),
});
export type BootstrapStatus = z.infer<typeof bootstrapSchema>;

export const patSchema = z.object({
  id: z.string().uuid(),
  name: z.string(),
  token_prefix: z.string().optional(),
  prefix: z.string().optional(),
  scopes: z.array(z.string()).default([]),
  repository_ids: z.array(z.string().uuid()).optional(),
  expires_at: z.string().datetime().nullable().optional(),
  revoked_at: z.string().datetime().nullable().optional(),
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
  membership?: { id: string; role: string; state: string };
  service_account_id?: string;
};
