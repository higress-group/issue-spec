import { z, type ZodType } from "zod";
import { cookieValue } from "../../lib/api/client";
import { commentSchema, issueSchema, labelSchema, reactionSchema, type ReactionContent } from "./types";

type Method = "GET" | "POST" | "PATCH" | "PUT" | "DELETE";
type Options<T> = { method?: Method; body?: unknown; schema?: ZodType<T>; signal?: AbortSignal };

const githubErrorSchema = z.object({
  message: z.string().default("Request failed"),
  errors: z.array(z.object({ resource: z.string(), field: z.string(), code: z.string(), message: z.string().optional() })).optional(),
});

export class IssueApiError extends Error {
  readonly status: number;
  readonly requestId?: string;
  readonly fields: string[];

  constructor(status: number, message: string, requestId?: string, fields: string[] = []) {
    super(message);
    this.name = "IssueApiError";
    this.status = status;
    this.requestId = requestId;
    this.fields = fields;
  }
}
async function request<T>(path: string, options: Options<T> = {}): Promise<T> {
  if (!path.startsWith("/") || path.startsWith("//")) throw new Error("Issue API paths must be same-origin");
  const target = new URL(path, window.location.origin);
  if (target.origin !== window.location.origin) throw new Error("Cross-origin issue API requests are forbidden");
  const method = options.method ?? "GET";
  const headers = new Headers({ Accept: "application/json", "X-Request-ID": globalThis.crypto?.randomUUID?.() ?? `issue-web-${Date.now()}` });
  if (options.body !== undefined) headers.set("Content-Type", "application/json");
  if (method !== "GET") {
    const csrf = cookieValue("issue_spec_csrf");
    if (csrf) headers.set("X-CSRF-Token", csrf);
  }
  const response = await fetch(target, {
    method,
    headers,
    body: options.body === undefined ? undefined : JSON.stringify(options.body),
    credentials: "same-origin",
    redirect: "error",
    signal: options.signal,
  });
  if (!response.ok) {
    const payload = await response.json().catch(() => ({ message: response.statusText || "Request failed" }));
    const parsed = githubErrorSchema.safeParse(payload);
    const envelope = parsed.success ? parsed.data : { message: response.statusText || "Request failed" };
    throw new IssueApiError(response.status, envelope.message, response.headers.get("X-Request-ID") ?? undefined,
      "errors" in envelope && envelope.errors ? envelope.errors.map((item) => `${item.field}: ${item.message ?? item.code}`) : []);
  }
  if (response.status === 204) return undefined as T;
  const payload: unknown = await response.json();
  return options.schema ? options.schema.parse(payload) : payload as T;
}

function base(owner: string, repo: string) {
  return `/repos/${encodeURIComponent(owner)}/${encodeURIComponent(repo)}`;
}

const issueListSchema = z.array(issueSchema);
const commentListSchema = z.array(commentSchema);
const labelListSchema = z.array(labelSchema);
const reactionListSchema = z.array(reactionSchema);

export const issueApi = {
  listIssues: (owner: string, repo: string, options: { state: string; labels: string[]; page: number; perPage?: number }, signal?: AbortSignal) => {
    const query = new URLSearchParams({ state: options.state, page: String(options.page), per_page: String(options.perPage ?? 20) });
    if (options.labels.length) query.set("labels", options.labels.join(","));
    return request(`${base(owner, repo)}/issues?${query}`, { schema: issueListSchema, signal });
  },
  getIssue: (owner: string, repo: string, number: number, signal?: AbortSignal) => request(`${base(owner, repo)}/issues/${number}`, { schema: issueSchema, signal }),
  createIssue: (owner: string, repo: string, body: { title: string; body: string; labels: string[] }) => request(`${base(owner, repo)}/issues`, { method: "POST", body, schema: issueSchema }),
  updateIssue: (owner: string, repo: string, number: number, body: { title?: string; body?: string; state?: "open" | "closed" }) => request(`${base(owner, repo)}/issues/${number}`, { method: "PATCH", body, schema: issueSchema }),
  listComments: (owner: string, repo: string, number: number, signal?: AbortSignal) => request(`${base(owner, repo)}/issues/${number}/comments?per_page=100`, { schema: commentListSchema, signal }),
  createComment: (owner: string, repo: string, number: number, body: string) => request(`${base(owner, repo)}/issues/${number}/comments`, { method: "POST", body: { body }, schema: commentSchema }),
  updateComment: (owner: string, repo: string, id: number, body: string) => request(`${base(owner, repo)}/issues/comments/${id}`, { method: "PATCH", body: { body }, schema: commentSchema }),
  listLabels: (owner: string, repo: string, signal?: AbortSignal) => request(`${base(owner, repo)}/labels?per_page=100`, { schema: labelListSchema, signal }),
  replaceLabels: (owner: string, repo: string, number: number, labels: string[]) => request(`${base(owner, repo)}/issues/${number}/labels`, { method: "PUT", body: { labels }, schema: labelListSchema }),
  listReactions: (owner: string, repo: string, commentId: number, signal?: AbortSignal) => request(`${base(owner, repo)}/issues/comments/${commentId}/reactions?per_page=100`, { schema: reactionListSchema, signal }),
  createReaction: (owner: string, repo: string, commentId: number, content: ReactionContent) => request(`${base(owner, repo)}/issues/comments/${commentId}/reactions`, { method: "POST", body: { content }, schema: reactionSchema }),
  deleteReaction: (owner: string, repo: string, commentId: number, reactionId: number) => request<void>(`${base(owner, repo)}/issues/comments/${commentId}/reactions/${reactionId}`, { method: "DELETE" }),
};

export function isIssueApiError(error: unknown, status?: number): error is IssueApiError {
  return error instanceof IssueApiError && (status === undefined || error.status === status);
}
