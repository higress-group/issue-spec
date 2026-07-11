import { z, type ZodType } from "zod";

export const problemSchema = z.object({
  type: z.string().optional(),
  title: z.string().default("Request failed"),
  status: z.number(),
  detail: z.string().optional(),
  code: z.string().default("request_failed"),
  request_id: z.string().optional(),
  meta: z.record(z.string(), z.unknown()).optional(),
});

export type Problem = z.infer<typeof problemSchema>;

export class ApiProblem extends Error {
  readonly problem: Problem;

  constructor(problem: Problem) {
    super(problem.detail || problem.title);
    this.name = "ApiProblem";
    this.problem = problem;
  }
}

export type RequestOptions<T> = {
  method?: "GET" | "POST" | "PATCH" | "DELETE";
  body?: unknown;
  schema?: ZodType<T>;
  signal?: AbortSignal;
};

const mutationMethods = new Set(["POST", "PATCH", "DELETE"]);

export async function apiRequest<T = unknown>(path: string, options: RequestOptions<T> = {}): Promise<T> {
  if (!path.startsWith("/") || path.startsWith("//")) {
    throw new Error("API paths must be same-origin absolute paths");
  }
  const target = new URL(path, window.location.origin);
  if (target.origin !== window.location.origin) {
    throw new Error("Cross-origin API requests are forbidden");
  }
  const method = options.method ?? "GET";
  const headers = new Headers({
    Accept: "application/json, application/problem+json",
    "X-Request-ID": globalThis.crypto?.randomUUID?.() ?? `web-${Date.now()}`,
  });
  if (options.body !== undefined) {
    headers.set("Content-Type", "application/json");
  }
  if (mutationMethods.has(method)) {
    const csrf = cookieValue("issue_spec_csrf");
    if (csrf) {
      headers.set("X-CSRF-Token", csrf);
    }
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
    let payload: unknown;
    try {
      payload = await response.json();
    } catch {
      payload = { status: response.status, title: response.statusText || "Request failed", code: "invalid_response" };
    }
    const parsed = problemSchema.safeParse(payload);
    throw new ApiProblem(parsed.success ? parsed.data : {
      status: response.status,
      title: response.statusText || "Request failed",
      code: "invalid_response",
      request_id: response.headers.get("X-Request-ID") ?? undefined,
    });
  }
  if (response.status === 204) {
    return undefined as T;
  }
  const payload: unknown = await response.json();
  return options.schema ? options.schema.parse(payload) : payload as T;
}

export function cookieValue(name: string): string | undefined {
  const prefix = `${encodeURIComponent(name)}=`;
  for (const part of document.cookie.split(";")) {
    const candidate = part.trim();
    if (candidate.startsWith(prefix)) {
      return decodeURIComponent(candidate.slice(prefix.length));
    }
  }
  return undefined;
}

export function isApiProblem(error: unknown, code?: string): error is ApiProblem {
  return error instanceof ApiProblem && (code === undefined || error.problem.code === code);
}
