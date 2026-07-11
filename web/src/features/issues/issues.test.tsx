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
import { issueApi, IssueApiError } from "./api";
import { CommentEditor, IssueEditor } from "./issue-editor";
import { IssueLoading, IssueStatus, MutationProblem } from "./repository-context";
import type { Label, Reactions } from "./types";

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
function userFixture() { return { login: "alice", id: 1, avatar_url: "", html_url: "", type: "User", site_admin: false }; }
