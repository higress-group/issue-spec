import { z } from "zod";

export const issueUserSchema = z.object({
  login: z.string(),
  name: z.string().optional().default(""),
  id: z.number(),
  avatar_url: z.string().optional().default(""),
  html_url: z.string().optional().default(""),
  type: z.string().default("User"),
  site_admin: z.boolean().default(false),
});

export const labelSchema = z.object({
  id: z.number(),
  name: z.string(),
  color: z.string().regex(/^[0-9a-fA-F]{6}$/),
  description: z.string().default(""),
  default: z.boolean().default(false),
  url: z.string().default(""),
});

export const reactionsSchema = z.object({
  total_count: z.number().default(0),
  "+1": z.number().default(0),
  "-1": z.number().default(0),
  laugh: z.number().default(0),
  hooray: z.number().default(0),
  confused: z.number().default(0),
  heart: z.number().default(0),
  rocket: z.number().default(0),
  eyes: z.number().default(0),
  url: z.string().default(""),
});

export const issueSchema = z.object({
  id: z.number(),
  number: z.number(),
  state: z.enum(["open", "closed"]),
  state_reason: z.string().nullable().optional(),
  title: z.string(),
  body: z.string().default(""),
  user: issueUserSchema,
  labels: z.array(labelSchema).default([]),
  locked: z.boolean().default(false),
  comments: z.number().default(0),
  created_at: z.string(),
  updated_at: z.string(),
  closed_at: z.string().nullable().optional(),
  html_url: z.string().default(""),
  reactions: reactionsSchema,
});

export const commentSchema = z.object({
  id: z.number(),
  body: z.string().default(""),
  user: issueUserSchema,
  created_at: z.string(),
  updated_at: z.string(),
  html_url: z.string().default(""),
  reactions: reactionsSchema,
});

export const choiceOptionSchema = z.object({
  id: z.string().regex(/^[a-z][a-z0-9-]{0,63}$/),
  label: z.string(),
  description: z.string().optional().default(""),
  tradeoff: z.string().optional().default(""),
}).strict();

export const choiceModelSchema = z.object({
  version: z.literal(1),
  mode: z.enum(["single", "multiple"]),
  options: z.array(choiceOptionSchema).max(20),
  allow_custom: z.boolean(),
}).strict();

export const questionSnapshotSchema = z.object({
  id: z.string().regex(/^QUESTION-[0-9]{3,}$/),
  question: z.string(),
  blocking: z.boolean(),
  default_assumption: z.string(),
  issue_url: z.string(),
  source_url: z.string(),
  choice_model: choiceModelSchema,
}).strict();

export const effectiveAnswerSchema = z.object({
  id: z.string(),
  comment_id: z.number().int().positive(),
  actor: z.string(),
  created_at: z.string(),
  selection: z.object({
    options: z.array(z.object({ id: z.string(), label: z.string() }).strict()).optional().default([]),
    custom: z.string().optional().default(""),
  }).strict(),
  source_url: z.string(),
}).strict();

export const questionAuthoritySchema = z.object({
  question: questionSnapshotSchema,
  representation_version: z.number().int().positive(),
  body_digest: z.string().regex(/^[0-9a-f]{64}$/),
  effective_answer: effectiveAnswerSchema.nullable().optional(),
}).strict();

export const answerResponseSchema = z.object({
  comment: commentSchema,
  question: questionSnapshotSchema,
  question_representation_version: z.number().int().positive(),
  question_body_digest: z.string().regex(/^[0-9a-f]{64}$/),
}).strict();

export const reactionContentSchema = z.enum(["+1", "-1", "laugh", "confused", "heart", "hooray", "rocket", "eyes"]);
export const reactionSchema = z.object({
  id: z.number(),
  user: issueUserSchema,
  content: reactionContentSchema,
  created_at: z.string(),
});

export type Issue = z.infer<typeof issueSchema>;
export type IssueComment = z.infer<typeof commentSchema>;
export type Label = z.infer<typeof labelSchema>;
export type Reactions = z.infer<typeof reactionsSchema>;
export type Reaction = z.infer<typeof reactionSchema>;
export type ReactionContent = z.infer<typeof reactionContentSchema>;
export type QuestionAuthority = z.infer<typeof questionAuthoritySchema>;
export type QuestionSnapshot = z.infer<typeof questionSnapshotSchema>;
export type EffectiveAnswer = z.infer<typeof effectiveAnswerSchema>;
