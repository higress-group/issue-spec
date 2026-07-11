import { z } from "zod";

export const changeStageSchema = z.enum(["unknown", "proposal", "design", "implement"]);
export const changeLifecycleSchema = z.enum(["active", "blocked", "completed", "closed"]);

export const boardRepositorySchema = z.object({
  id: z.string().uuid(),
  name: z.string(),
  display_name: z.string(),
});

export const artifactSchema = z.object({
  id: z.string().uuid(),
  number: z.number().int().positive(),
  title: z.string(),
  state: z.string(),
  url: z.string(),
  marker_version: z.string(),
  updated_at: z.string().datetime({ offset: true }),
  valid: z.boolean(),
});

export const progressSchema = z.object({
  total: z.number().int().nonnegative(),
  completed: z.number().int().nonnegative(),
  in_progress: z.number().int().nonnegative(),
  blocked: z.number().int().nonnegative(),
  pending: z.number().int().nonnegative(),
});

export const changeCardSchema = z.object({
  repository: boardRepositorySchema,
  change_key: z.string(),
  title: z.string(),
  current_stage: changeStageSchema,
  lifecycle: changeLifecycleSchema,
  artifacts: z.object({
    proposal: artifactSchema.optional(),
    design: artifactSchema.optional(),
    implement: artifactSchema.optional(),
  }),
  tasks: progressSchema,
  processes: progressSchema,
  anomalies: z.array(z.string()).default([]),
  updated_at: z.string().datetime({ offset: true }),
});

export const boardPageSchema = z.object({
  cards: z.array(changeCardSchema),
  page: z.number().int().positive(),
  per_page: z.number().int().positive(),
  total: z.number().int().nonnegative(),
  counts: z.object({
    total: z.number().int().nonnegative(),
    active: z.number().int().nonnegative(),
    blocked: z.number().int().nonnegative(),
    completed: z.number().int().nonnegative(),
    closed: z.number().int().nonnegative(),
    proposal: z.number().int().nonnegative(),
    design: z.number().int().nonnegative(),
    implement: z.number().int().nonnegative(),
    unknown: z.number().int().nonnegative(),
  }),
  diagnostics: z.array(z.object({ code: z.string(), count: z.number().int().nonnegative() })).default([]),
});

export type ChangeStage = z.infer<typeof changeStageSchema>;
export type ChangeLifecycle = z.infer<typeof changeLifecycleSchema>;
export type Artifact = z.infer<typeof artifactSchema>;
export type Progress = z.infer<typeof progressSchema>;
export type ChangeCardModel = z.infer<typeof changeCardSchema>;
export type BoardPageModel = z.infer<typeof boardPageSchema>;

export type BoardFilters = {
  stage?: ChangeStage;
  lifecycle?: ChangeLifecycle;
  anomaly?: string;
  page: number;
  perPage: number;
};

export const anomalyCatalog: Record<string, { label: string; description: string }> = {
  duplicate_artifact_type: { label: "Duplicate stage", description: "More than one artifact claims the same workflow stage." },
  marker_label_mismatch: { label: "Marker and label drift", description: "The issue marker and its display label describe different stages." },
  missing_required_links: { label: "Required link missing", description: "An artifact does not link to the workflow issue that precedes it." },
  unsupported_marker_version: { label: "Unsupported marker", description: "The artifact marker version cannot be projected safely." },
  implement_missing_predecessor: { label: "Predecessor missing", description: "Implementation exists without a valid design artifact." },
  orphan_typed_artifact: { label: "Detached workflow record", description: "A typed workflow record is not attached to a projected change." },
  malformed_issue_marker: { label: "Malformed issue marker", description: "An artifact marker could not be parsed into a change." },
};

export function anomalyCopy(code: string) {
  return anomalyCatalog[code] ?? { label: "Projection diagnostic", description: "The server reported a workflow projection diagnostic." };
}
