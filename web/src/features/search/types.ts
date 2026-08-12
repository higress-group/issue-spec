import { z } from "zod";

export const searchStateSchema = z.enum(["all", "open", "closed"]);

export const searchMatchSchema = z.object({
  source: z.literal("issue"),
  excerpt: z.string(),
});

export const searchChangeSchema = z.object({ key: z.string().min(1), stage: z.enum(["proposal", "design", "implement", "unknown"]), matched: z.boolean() });

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
  page: number;
  perPage: number;
};
