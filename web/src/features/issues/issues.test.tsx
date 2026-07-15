import axe from "axe-core";
import { http, HttpResponse } from "msw";
import { screen, waitFor } from "@testing-library/react";
import userEvent from "@testing-library/user-event";
import { describe, expect, it, vi } from "vitest";
import { MarkdownView } from "../../components/markdown/markdown-view";
import { stripIssueSpecMarkersForRender } from "../../components/markdown/issue-markers";
import { LabelSelector } from "../../components/labels/label-chips";
import { ReactionPicker } from "../../components/reactions/reaction-picker";
import { renderApp } from "../../../tests/render";
import { server } from "../../../tests/server";
import { Route, Routes } from "react-router-dom";
import { issueApi, IssueApiError } from "./api";
import { CommentEditor, IssueEditor } from "./issue-editor";
import { IssueDetail } from "./detail-page";
import { IssueLoading, IssueStatus, MutationProblem } from "./repository-context";
import type { ActiveRepository } from "./repository-context";
import type { Label, Reactions } from "./types";
import type { CodeChangeRelationship } from "../../lib/api/relationships";

const label: Label = { id: 1, name: "issue-spec/design", color: "62459a", description: "Design", default: false, url: "" };
const reactions: Reactions = { total_count: 1, "+1": 1, "-1": 0, laugh: 0, hooray: 0, confused: 0, heart: 0, rocket: 0, eyes: 0, url: "" };

describe("secure issue markdown", () => {
  it("hides only exact standalone workflow markers outside fenced code", () => {
    const exact = "<!-- issue-spec:type=PROCESS id=PROCESS-010 version=1 -->";
    const source = `Visible\n${exact}\nInline ${exact}\n\`\`\`md\n${exact}\n\`\`\``;
    expect(stripIssueSpecMarkersForRender(source)).toBe(`Visible\nInline ${exact}\n\`\`\`md\n${exact}\n\`\`\``);
  });

  it("renders GFM but strips scripts, event handlers, active URLs, SVG, iframe and style", async () => {
    const source = `| A | B |\n|---|---|\n| one | two |\n- [x] done\n\n[bad](javascript:alert(1)) [external](https://example.test)\n\n<script>alert(1)</script><img src="data:text/html,bad" onerror="alert(2)"><iframe src="https://evil.test"></iframe><svg onload="alert(3)"></svg><style>body{display:none}</style>\n\n\`\`\`js\nconst safe = true\n\`\`\``;
    const { container } = renderApp(<MarkdownView source={source} />);
    expect(await screen.findByRole("table")).toBeInTheDocument();
    expect(screen.getByRole("checkbox")).toBeChecked();
    const external = screen.getByRole("link", { name: "external" });
    expect(external).toHaveAttribute("rel", "noopener noreferrer");
    expect(external).toHaveAttribute("target", "_blank");
    expect(external).toHaveAttribute("referrerpolicy", "no-referrer");
    expect(container.querySelector("script,iframe,svg,style")).toBeNull();
    expect(container.querySelector("[onerror],[onload]")).toBeNull();
    expect(screen.getByText("bad").closest("a")).not.toHaveAttribute("href");
    expect(container.querySelector("img")).toHaveAttribute("loading", "lazy");
    expect(container.querySelector("img")).toHaveAttribute("referrerpolicy", "no-referrer");
    expect(container.querySelector("code.hljs")).toBeInTheDocument();
    expect((await axe.run(container)).violations).toEqual([]);
  });

  it("keeps same-page comment permalinks in the current tab", () => {
    const source = `[same comment](http://localhost/#issuecomment-9) [another issue](http://localhost/other#issuecomment-9) [external](https://example.test/#issuecomment-9)`;
    renderApp(<MarkdownView source={source} />);

    const sameComment = screen.getByRole("link", { name: "same comment" });
    expect(sameComment).not.toHaveAttribute("target");
    expect(sameComment).not.toHaveAttribute("rel");
    expect(screen.getByRole("link", { name: "another issue" })).toHaveAttribute("target", "_blank");
    expect(screen.getByRole("link", { name: "external" })).toHaveAttribute("target", "_blank");
  });
});

describe("issue editing semantics", () => {
  it("keeps the raw issue body byte-for-byte while preview hides its marker", async () => {
    const raw = "  first line\r\n<!-- issue-spec:issue=proposal change=raw-body version=1 -->\r\nlast  ";
    const submit = vi.fn();
    renderApp(<IssueEditor initial={{ title: "Raw body", body: raw, labels: [] }} labels={[label]} submitLabel="Save" pending={false} onSubmit={submit} />);
    const user = userEvent.setup();
    expect(screen.getByRole("textbox", { name: "Description" })).toHaveValue(raw.replaceAll("\r\n", "\n"));
    await user.click(screen.getByRole("tab", { name: "Preview" }));
    expect(screen.getByTestId("rendered-markdown")).not.toHaveTextContent("issue-spec:issue");
    await user.click(screen.getByRole("tab", { name: "Write" }));
    await user.click(screen.getByRole("button", { name: "Save" }));
    expect(submit).toHaveBeenCalledWith({ title: "Raw body", body: raw, labels: [] });
  });

  it("preserves a comment draft and explains a 409 conflict", async () => {
    renderApp(<CommentEditor initial="unsent decision" pending={false} error={new IssueApiError(409, "Conflict")} onSubmit={() => undefined} />);
    expect(screen.getByRole("textbox", { name: "Comment" })).toHaveValue("unsent decision");
    expect(screen.getByRole("alert")).toHaveTextContent("Your draft is still here");
    expect(screen.getByRole("button", { name: "Reload latest" })).toBeInTheDocument();
  });

  it("assigns and removes labels with keyboard-operable controls", async () => {
    const change = vi.fn();
    renderApp(<LabelSelector labels={[label]} selected={[]} onChange={change} />);
    await userEvent.setup().click(screen.getByRole("checkbox", { name: "issue-spec/design" }));
    expect(change).toHaveBeenCalledWith(["issue-spec/design"]);
  });

  it("presents loading, forbidden, not-found and generic failure states explicitly", () => {
    const loading = renderApp(<IssueLoading />);
    expect(screen.getByRole("status")).toHaveTextContent("Loading issues");
    loading.unmount();
    const forbidden = renderApp(<IssueStatus status={403} />);
    expect(screen.getByRole("heading", { name: /outside your authority/i })).toBeInTheDocument();
    forbidden.unmount();
    const missing = renderApp(<IssueStatus status={404} />);
    expect(screen.getByRole("heading", { name: /not here/i })).toBeInTheDocument();
    missing.unmount();
    renderApp(<MutationProblem error={new IssueApiError(500, "Failed")} />);
    expect(screen.getByRole("alert")).toHaveTextContent("draft remains");
  });
});

describe("canonical issue read authority", () => {
  it("copies a canonical comment permalink and falls back when Clipboard API writes fail", async () => {
    installIssueDetailHandlers();
    const user = userEvent.setup();
    const clipboardDescriptor = Object.getOwnPropertyDescriptor(navigator, "clipboard");
    const execCommandDescriptor = Object.getOwnPropertyDescriptor(document, "execCommand");
    const writeText = vi.fn().mockRejectedValue(new Error("insecure context"));
    let copiedValue = "";
    Object.defineProperty(navigator, "clipboard", { configurable: true, value: { writeText } });
    Object.defineProperty(document, "execCommand", { configurable: true, value: vi.fn(() => {
      copiedValue = document.querySelector<HTMLTextAreaElement>("textarea[readonly]")?.value ?? "";
      return true;
    }) });
    try {
      renderIssueDetail(activeRepository(false, ["read"]));
      const copy = await screen.findByRole("button", { name: "Copy link" });
      await user.click(copy);
      const permalink = "http://localhost/acme/workflow/issues/41#issuecomment-9";
      expect(writeText).toHaveBeenCalledWith(permalink);
      expect(copiedValue).toBe(permalink);
      expect(screen.getByRole("button", { name: "Link copied" })).toBeVisible();
    } finally {
      if (clipboardDescriptor) Object.defineProperty(navigator, "clipboard", clipboardDescriptor);
      else Reflect.deleteProperty(navigator, "clipboard");
      if (execCommandDescriptor) Object.defineProperty(document, "execCommand", execCommandDescriptor);
      else Reflect.deleteProperty(document, "execCommand");
    }
  });

  it("renders anonymous public issue content without any mutation controls", async () => {
    installIssueDetailHandlers([relationshipFixture("github", "42")]);
    const { container } = renderIssueDetail(activeRepository(false, ["read"]));
    expect(await screen.findByRole("heading", { name: /Runner contract/ })).toBeVisible();
    expect(screen.queryByRole("button", { name: /^Edit$/ })).not.toBeInTheDocument();
    expect(screen.queryByRole("button", { name: "Close" })).not.toBeInTheDocument();
    expect(screen.queryByRole("textbox", { name: "Comment" })).not.toBeInTheDocument();
    expect(screen.queryByText("Manage labels")).not.toBeInTheDocument();
    expect(screen.queryByRole("button", { name: /reaction/i })).not.toBeInTheDocument();
    expect(screen.getByRole("heading", { name: "Read-only conversation" })).toBeVisible();
    expect(screen.getByRole("link", { name: "Sign in" })).toHaveAttribute("href", "/login");
    const relationship = await screen.findByRole("link", { name: /Runner projection/ });
    expect(screen.getByText("Pull request")).toBeVisible();
    expect(relationship).toHaveAttribute("target", "_blank");
    expect(relationship).toHaveAttribute("referrerpolicy", "no-referrer");
    expect(container).not.toHaveTextContent("head_revision");
    expect((await axe.run(container)).violations).toEqual([]);
  });

  it("renders a deleted author as text without linking an unrelated ghost account", async () => {
    installIssueDetailHandlers();
    const ghost = { login: "ghost", name: "ghost", id: 1, avatar_url: "", html_url: "", type: "User", site_admin: false };
    server.use(
      http.get("http://localhost/repos/acme/workflow/issues/41", () => HttpResponse.json(issueFixture({ user: ghost }))),
      http.get("http://localhost/repos/acme/workflow/issues/41/comments", () => HttpResponse.json([commentFixture({ user: ghost })])),
    );
    renderIssueDetail(activeRepository(false, ["read"]));
    expect(await screen.findByRole("heading", { name: /Runner contract/ })).toBeVisible();
    for (const identity of screen.getAllByText("@ghost")) {
      expect(identity.closest("a")).toBeNull();
    }
  });

  it("shows authenticated mutations only when allowed_actions grants them", async () => {
    installIssueDetailHandlers([relationshipFixture("github", "42"), relationshipFixture("aone-bridge", "73", "mismatched", { head_revision: "abc123" })]);
    renderIssueDetail(activeRepository(true, ["read", "contribute", "triage"]));
    expect(await screen.findByRole("heading", { name: /Runner contract/ })).toBeVisible();
    expect(screen.getByRole("button", { name: /^Edit$/ })).toBeVisible();
    expect(screen.getByRole("button", { name: "Close" })).toBeVisible();
    expect(screen.getByRole("textbox", { name: "Comment" })).toBeVisible();
    expect(screen.getByText("Manage labels")).toBeVisible();
    expect(await screen.findByRole("button", { name: /Remove Thumbs up reaction/ })).toBeVisible();
    expect(await screen.findByText("Merge request")).toBeVisible();
    expect(screen.getByText("Binding mismatch")).toBeVisible();
    expect(screen.queryByText("abc123")).not.toBeInTheDocument();
  });
});

describe("GitHub-compatible issue API", () => {
  it("sends filters and round-trips raw bodies without trimming", async () => {
    let query = "";
    let created: unknown;
    const raw = "\n<!-- issue-spec:type=TASK id=TASK-010 version=1 -->\n  keep me  \n";
    server.use(
      http.get("http://localhost/repos/acme/workflow/issues", ({ request }) => { query = new URL(request.url).search; return HttpResponse.json([]); }),
      http.post("http://localhost/repos/acme/workflow/issues", async ({ request }) => { created = await request.json(); return HttpResponse.json(issueFixture({ body: raw }), { status: 201 }); }),
    );
    await issueApi.listIssues("acme", "workflow", { state: "closed", labels: ["design", "question"], page: 3 });
    expect(query).toContain("state=closed");
    expect(query).toContain("labels=design%2Cquestion");
    expect(query).toContain("page=3");
    const response = await issueApi.createIssue("acme", "workflow", { title: "Raw", body: raw, labels: [] });
    expect(created).toEqual({ title: "Raw", body: raw, labels: [] });
    expect(response.body).toBe(raw);
  });

  it("loads code-change relationships through the canonical optional-auth route", async () => {
    let path = "";
    server.use(http.get("http://localhost/api/v1/context/repos/acme/workflow/issues/41/relationships", ({ request }) => {
      path = new URL(request.url).pathname;
      return HttpResponse.json({ relationships: [relationshipFixture("github", "42")] });
    }));
    const response = await issueApi.getRelationships(" acme ", " workflow ", 41);
    expect(path).toBe("/api/v1/context/repos/acme/workflow/issues/41/relationships");
    expect(response.relationships[0]).toMatchObject({ provider_key: "github", relation_kind: "code_change", source_binding_match: "matched" });
  });

  it("covers issue/comment CRUD, labels, reactions, and compatible errors", async () => {
    const methods: string[] = [];
    server.use(
      http.patch("http://localhost/repos/acme/workflow/issues/41", ({ request }) => { methods.push(request.method); return HttpResponse.json(issueFixture({ state: "closed" })); }),
      http.post("http://localhost/repos/acme/workflow/issues/41/comments", ({ request }) => { methods.push(request.method); return HttpResponse.json(commentFixture(), { status: 201 }); }),
      http.patch("http://localhost/repos/acme/workflow/issues/comments/9", ({ request }) => { methods.push(request.method); return HttpResponse.json(commentFixture({ body: "edited" })); }),
      http.put("http://localhost/repos/acme/workflow/issues/41/labels", ({ request }) => { methods.push(request.method); return HttpResponse.json([label]); }),
      http.post("http://localhost/repos/acme/workflow/issues/comments/9/reactions", ({ request }) => { methods.push(request.method); return HttpResponse.json(reactionFixture(), { status: 201 }); }),
      http.delete("http://localhost/repos/acme/workflow/issues/comments/9/reactions/7", ({ request }) => { methods.push(request.method); return new HttpResponse(null, { status: 204 }); }),
      http.patch("http://localhost/repos/acme/workflow/issues/99", () => HttpResponse.json({ message: "Conflict" }, { status: 409, headers: { "X-Request-ID": "conflict-request" } })),
    );
    await issueApi.updateIssue("acme", "workflow", 41, { state: "closed" });
    await issueApi.createComment("acme", "workflow", 41, "hello");
    await issueApi.updateComment("acme", "workflow", 9, "edited");
    await issueApi.replaceLabels("acme", "workflow", 41, [label.name]);
    await issueApi.createReaction("acme", "workflow", 9, "+1");
    await issueApi.deleteReaction("acme", "workflow", 9, 7);
    expect(methods).toEqual(["PATCH", "POST", "PATCH", "PUT", "POST", "DELETE"]);
    await expect(issueApi.updateIssue("acme", "workflow", 99, { title: "conflict" })).rejects.toMatchObject({ status: 409, requestId: "conflict-request" });
  });

  it("removes the current user's existing reaction instead of duplicating it", async () => {
    let deleted = false;
    server.use(
      http.get("http://localhost/repos/acme/workflow/issues/comments/9/reactions", () => HttpResponse.json([reactionFixture()])),
      http.delete("http://localhost/repos/acme/workflow/issues/comments/9/reactions/7", () => { deleted = true; return new HttpResponse(null, { status: 204 }); }),
    );
    renderApp(<ReactionPicker owner="acme" repo="workflow" commentId={9} summary={reactions} currentLogin="alice" />);
    const button = await screen.findByRole("button", { name: /Remove Thumbs up/ });
    await userEvent.setup().click(button);
    await waitFor(() => expect(deleted).toBe(true));
  });
});

function issueFixture(overrides: Record<string, unknown> = {}) {
  return { id: 41, number: 41, state: "open", state_reason: null, title: "Runner contract", body: "Body", user: userFixture(), labels: [label], locked: false, comments: 1, created_at: "2026-07-10T10:00:00Z", updated_at: "2026-07-10T10:00:00Z", closed_at: null, html_url: "https://example.test/acme/workflow/issues/41", reactions, ...overrides };
}

function commentFixture(overrides: Record<string, unknown> = {}) {
  return { id: 9, body: "Comment", user: userFixture(), created_at: "2026-07-10T11:00:00Z", updated_at: "2026-07-10T11:00:00Z", html_url: "https://example.test/acme/workflow/issues/41#issuecomment-9", reactions, ...overrides };
}

function reactionFixture() { return { id: 7, user: userFixture(), content: "+1", created_at: "2026-07-10T11:30:00Z" }; }
function userFixture() { return { login: "alice", id: 1, avatar_url: "", html_url: "http://localhost/users/alice", type: "User", site_admin: false }; }

function installIssueDetailHandlers(relationships: CodeChangeRelationship[] = []) {
  server.use(
    http.get("http://localhost/repos/acme/workflow/issues/41", () => HttpResponse.json(issueFixture())),
    http.get("http://localhost/repos/acme/workflow/issues/41/comments", () => HttpResponse.json([commentFixture()])),
    http.get("http://localhost/repos/acme/workflow/labels", () => HttpResponse.json([label])),
    http.get("http://localhost/repos/acme/workflow/issues/comments/9/reactions", () => HttpResponse.json([reactionFixture()])),
    http.get("http://localhost/api/v1/context/repos/acme/workflow/issues/41/relationships", () => HttpResponse.json({ relationships })),
  );
}

function relationshipFixture(provider = "github", externalId = "42", sourceBindingMatch: CodeChangeRelationship["source_binding_match"] = "matched", metadata?: Record<string, unknown>): CodeChangeRelationship {
  return {
    provider_key: provider,
    code_change_label: provider === "github" ? "Pull request" : provider === "aone-bridge" ? "Merge request" : "Code change",
    relation_kind: "code_change",
    external_repository_id: provider === "aone-bridge" ? "Ingress/issue-spec" : "higress-group/issue-spec",
    external_id: externalId,
    canonical_url: `https://code.example/${provider}/changes/${externalId}`,
    title: provider === "github" ? "Runner projection" : "Provider merge",
    lifecycle_state: "active",
    source_binding_match: sourceBindingMatch,
    ...(metadata ? { metadata } : {}),
  };
}

function renderIssueDetail(active: ActiveRepository) {
  return renderApp(<Routes><Route path="/:owner/:repo/issues/:number" element={<IssueDetail active={active} />} /></Routes>, "/acme/workflow/issues/41");
}

function activeRepository(authenticated: boolean, allowed_actions: string[]): ActiveRepository {
  return {
    authenticated,
    organization: { id: "bbbbbbbb-bbbb-4bbb-8bbb-bbbbbbbbbbbb", name: "acme", display_name: "Acme", effective_permission: "read", container_only: !authenticated, allowed_actions: [] },
    repository: { repository: { id: "cccccccc-cccc-4ccc-8ccc-cccccccccccc", organization_id: "bbbbbbbb-bbbb-4bbb-8bbb-bbbbbbbbbbbb", name: "workflow", display_name: "Workflow", visibility: "public", contribution_policy: "authenticated" }, effective_permission: authenticated ? "triage" : "read", allowed_actions },
  };
}
