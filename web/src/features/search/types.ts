import { z } from "zod";

export const searchStateSchema = z.enum(["all", "open", "closed"]);
export const searchSourceSchema = z.enum(["all", "issue", "comments", "change"]);
export const searchStageSchema = z.enum(["proposal", "design", "implement"]);

export const searchMatchSchema = z.object({
  source: z.enum(["issue", "comment", "change"]),
  excerpt: z.string(),
  comment_id: z.string().uuid().optional(),
});

export const searchChangeSchema = z.object({ key: z.string().min(1), stage: z.enum(["proposal", "design", "implement", "unknown"]) });

export const searchIssueSchema = z.object({
  organization_id: z.string().uuid(),
  organization: z.string().min(1),
  repository_id: z.string().uuid(),
  repository: z.string().min(1),
  id: z.string().uuid(),
  number: z.number().int().positive(),
  title: z.string(),
  state: z.enum(["open", "closed"]),
  updated_at: z.string().datetime({ offset: true }),
  url: z.string().url(),
  changes: z.array(searchChangeSchema),
  score: z.number().int(),
  matches: z.array(searchMatchSchema).max(5),
});

export const searchPageSchema = z.object({
  items: z.array(searchIssueSchema),
  page: z.number().int().positive(),
  per_page: z.number().int().min(1).max(50),
  total: z.number().int().nonnegative(),
  has_next: z.boolean(),
});

export type SearchPageModel = z.infer<typeof searchPageSchema>;
export type SearchIssueModel = z.infer<typeof searchIssueSchema>;
export type SearchFilters = {
  query: string;
  state: z.infer<typeof searchStateSchema>;
  source: z.infer<typeof searchSourceSchema>;
  stage?: z.infer<typeof searchStageSchema>;
  page: number;
  perPage: number;
};
