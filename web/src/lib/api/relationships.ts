import { z } from "zod";

export const sourceBindingMatchSchema = z.enum(["matched", "mismatched", "unbound"]);

export const codeChangeRelationshipSchema = z.object({
  provider_key: z.string().trim().min(1),
  code_change_label: z.string().trim().min(1).optional().default("Code change"),
  relation_kind: z.literal("code_change"),
  external_repository_id: z.string().trim().min(1),
  external_id: z.string().trim().min(1),
  canonical_url: z.string().min(1),
  title: z.string().optional(),
  lifecycle_state: z.literal("active"),
  source_binding_match: sourceBindingMatchSchema,
  metadata: z.record(z.string(), z.unknown()).optional(),
});

export const issueRelationshipsSchema = z.object({
  relationships: z.array(codeChangeRelationshipSchema).default([]),
});

export type SourceBindingMatch = z.infer<typeof sourceBindingMatchSchema>;
export type CodeChangeRelationship = z.infer<typeof codeChangeRelationshipSchema>;
export type IssueRelationships = z.infer<typeof issueRelationshipsSchema>;

export function codeChangeKind(relationship: Pick<CodeChangeRelationship, "code_change_label">) {
  return relationship.code_change_label.trim();
}

export function safeCodeChangeURL(value: string): string | undefined {
  const hasControl = [...value].some((character) => character.charCodeAt(0) <= 31 || character.charCodeAt(0) === 127);
  if (!value || value !== value.trim() || value.includes("\\") || value.includes("?") || value.includes("#") || hasControl) return undefined;
  try {
    const parsed = new URL(value);
    if (parsed.protocol !== "https:" || !parsed.hostname || parsed.username || parsed.password ||
      parsed.search || parsed.hash || parsed.hostname.endsWith(".") || parsed.port === "443") return undefined;
    // URL adds a slash to an origin-only URL. Apart from that harmless case,
    // reject every normalization so encoded credentials, dot segments, mixed
    // case hosts and non-canonical ports never become clickable.
    const normalized = parsed.pathname === "/" && value === parsed.origin ? parsed.origin : parsed.href;
    return normalized === value ? value : undefined;
  } catch {
    return undefined;
  }
}
