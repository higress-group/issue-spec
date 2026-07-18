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
import { IssueList } from "./list-page";
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

  it("links canonical prose mentions without touching code, links, URLs, or email", () => {
    const source = "Hello @Alice `@code` [@label](https://example.test/profile) https://example.test/@path user@example.test";
    renderApp(<MarkdownView source={source} />);
    const mention = screen.getByRole("link", { name: "@Alice" });
    expect(mention).toHaveAttribute("href", "/users/alice");
    expect(mention).not.toHaveAttribute("target");
    expect(screen.getByText("@code").closest("a")).toBeNull();
    expect(screen.getByRole("link", { name: "@label" })).toHaveAttribute("href", "https://example.test/profile");
    expect(screen.queryByRole("link", { name: "@path" })).not.toBeInTheDocument();
    expect(screen.queryByRole("link", { name: "@example" })).not.toBeInTheDocument();
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

  it("debounces mention discovery and inserts the selected canonical login", async () => {
    let requestedPrefix = "";
    server.use(http.get("http://localhost/api/v1/mentions/candidates", ({ request }) => {
      requestedPrefix = new URL(request.url).searchParams.get("q") ?? "";
      return HttpResponse.json([
        { login: "alice", display_name: "Alice", avatar_url: "http://localhost/api/v1/avatars/alice" },
        { login: "alicia", display_name: "Alicia", avatar_url: "http://localhost/api/v1/avatars/alicia" },
      ]);
    }));
    const submit = vi.fn();
    renderApp(<CommentEditor pending={false} onSubmit={submit} />);
    const user = userEvent.setup();
    const editor = screen.getByRole("textbox", { name: "Comment" });
    await user.type(editor, "Hello @ali");
    const suggestions = await screen.findByRole("listbox", { name: "Mention suggestions" });
    expect(requestedPrefix).toBe("ali");
    expect(suggestions).toHaveAttribute("id");
    expect(editor).toHaveAttribute("aria-controls", suggestions.id);
    expect(editor).toHaveAttribute("aria-activedescendant");
    expect(editor).not.toHaveAttribute("aria-expanded");
    expect(editor).not.toHaveAttribute("aria-haspopup");
    await user.keyboard("{ArrowDown}{Enter}");
    expect(editor).toHaveValue("Hello @alicia ");
    expect(editor).toHaveFocus();
    await user.click(screen.getByRole("button", { name: "Comment" }));
    expect(submit).toHaveBeenCalledWith("Hello @alicia ");
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
    let subscriptionRequests = 0;
    server.use(
      repositorySubscriptionMetaHandler(),
      http.get("http://localhost/api/v1/profile/email", () => { subscriptionRequests += 1; return HttpResponse.json({ available: true, notification_email: "reader@example.test" }); }),
      http.get("http://localhost/api/v1/orgs/:orgId/repos/:repoId/subscription", () => { subscriptionRequests += 1; return HttpResponse.json(repositorySubscriptionFixture()); }),
    );
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
    expect(screen.queryByRole("button", { name: "Subscribe to repository" })).not.toBeInTheDocument();
    expect(screen.queryByRole("link", { name: "Set email for repository notifications" })).not.toBeInTheDocument();
    expect(subscriptionRequests).toBe(0);
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

  it("shares relative issue time and shows edited time only for comments actually edited", async () => {
    vi.spyOn(Date, "now").mockReturnValue(Date.parse("2026-07-10T11:08:00Z"));
    const clockIntervals = vi.spyOn(window, "setInterval");
    server.use(
      http.get("http://localhost/repos/acme/workflow/issues/41", () => HttpResponse.json(issueFixture({ updated_at: "2026-07-10T11:07:00Z" }))),
      http.get("http://localhost/repos/acme/workflow/issues/41/comments", () => HttpResponse.json([commentFixture({ updated_at: "2026-07-10T11:04:00Z" })])),
      http.get("http://localhost/repos/acme/workflow/labels", () => HttpResponse.json([label])),
      http.get("http://localhost/repos/acme/workflow/issues/comments/9/reactions", () => HttpResponse.json([])),
      http.get("http://localhost/api/v1/context/repos/acme/workflow/issues/41/relationships", () => HttpResponse.json({ relationships: [] })),
    );
    const { container } = renderIssueDetail(activeRepository(false, ["read"]));
    expect(await screen.findByRole("heading", { name: /Runner contract/ })).toBeVisible();
    expect(await screen.findByText("Comment")).toBeVisible();
    expect(container.querySelector(".detail-state-row")?.textContent).toContain("@alice opened this 1h ago");
    const commentMetadata = container.querySelector("#issuecomment-9 header small");
    expect(commentMetadata).toHaveTextContent("commented 8m ago · edited 4m ago");
    expect(container.querySelectorAll("#issuecomment-9 time")).toHaveLength(2);
    expect(container.textContent?.match(/edited/g)).toHaveLength(1);
    expect(clockIntervals.mock.calls.filter(([, delay]) => delay === 1_000)).toHaveLength(1);
    for (const time of container.querySelectorAll("time")) {
      expect(time).toHaveAttribute("datetime");
      expect(time).toHaveAttribute("title", expect.stringMatching(/:\d{2}:\d{2}/));
      expect(time).toHaveAttribute("aria-label", expect.stringContaining(time.title));
    }
  });

  it("renders the issue list opened time as a structured relative time node", async () => {
    vi.spyOn(Date, "now").mockReturnValue(Date.parse("2026-07-10T10:08:00Z"));
    installIssueListHandlers();
    const { container } = renderIssueList(activeRepository(false, ["read"]));
    expect(await screen.findByRole("heading", { name: "Issues" })).toBeVisible();
    expect(container.querySelector(".issue-meta")).toHaveTextContent("#41 opened 8m ago by @alice");
    const time = container.querySelector<HTMLTimeElement>(".issue-meta time")!;
    const issueLink = container.querySelector<HTMLAnchorElement>(".issue-list a")!;
    expect(time).toHaveAttribute("datetime", "2026-07-10T10:00:00.000Z");
    expect(time).not.toHaveAttribute("tabindex");
    expect(container.querySelector(".precise-time-disclosure")).not.toBeInTheDocument();
    const user = userEvent.setup();
    for (let tab = 0; tab < 10 && document.activeElement !== issueLink; tab += 1) await user.tab();
    expect(issueLink).toHaveFocus();
    expect(time).toHaveTextContent("8m ago");
    expect(container.querySelector(".precise-time-disclosure")).toHaveTextContent(time.title);
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
    expect(screen.queryByRole("button", { name: "Subscribe to repository" })).not.toBeInTheDocument();
  });

  it("offers repository subscription on the issue list to a reader without triage authority", async () => {
    server.use(
      repositorySubscriptionMetaHandler(),
      http.get("http://localhost/api/v1/profile/email", () => HttpResponse.json({ available: true, notification_email: "reader@example.test" })),
      http.get("http://localhost/api/v1/orgs/:orgId/repos/:repoId/subscription", () => HttpResponse.json(repositorySubscriptionFixture())),
    );
    installIssueListHandlers();
    renderIssueList(activeRepository(true, ["read"]));
    expect(await screen.findByRole("button", { name: "Subscribe to repository" })).toHaveAttribute("aria-pressed", "false");
    expect(screen.queryByRole("link", { name: "New issue" })).not.toBeInTheDocument();
  });

  it("keeps the account-settings guide on the issue list when a reader has no verified email", async () => {
    server.use(
      repositorySubscriptionMetaHandler(),
      http.get("http://localhost/api/v1/profile/email", () => HttpResponse.json({ available: true, notification_email: null })),
      http.get("http://localhost/api/v1/orgs/:orgId/repos/:repoId/subscription", () => HttpResponse.json(repositorySubscriptionFixture())),
    );
    installIssueListHandlers();
    renderIssueList(activeRepository(true, ["read"]));
    expect(await screen.findByRole("link", { name: "Set email for repository notifications" })).toHaveAttribute("href", "/settings/account");
    expect(screen.queryByRole("button", { name: "Subscribe to repository" })).not.toBeInTheDocument();
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

function installIssueListHandlers() {
  server.use(
    http.get("http://localhost/repos/acme/workflow/issues", () => HttpResponse.json([issueFixture()])),
    http.get("http://localhost/repos/acme/workflow/labels", () => HttpResponse.json([label])),
  );
}

function repositorySubscriptionMetaHandler() {
  return http.get("http://localhost/api/v1/meta", () => HttpResponse.json({
    api_version: "v1",
    features: {
      bootstrap: true, personal_access_tokens: true, organizations: true, source_bindings: false,
      webhooks: false, change_boards: false, runner: false, recovery_exchange: true,
      email_notifications: true, repository_email_subscriptions: true,
    },
  }));
}

function repositorySubscriptionFixture() {
  return { subscribed: false, ignored: false, reason: "", representation_version: 0, collection_version: 1 };
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

function renderIssueList(active: ActiveRepository) {
  return renderApp(<Routes><Route path="/:owner/:repo/issues" element={<IssueList active={active} />} /></Routes>, "/acme/workflow/issues");
}

function activeRepository(authenticated: boolean, allowed_actions: string[]): ActiveRepository {
  return {
    authenticated,
    organization: { id: "bbbbbbbb-bbbb-4bbb-8bbb-bbbbbbbbbbbb", name: "acme", display_name: "Acme", effective_permission: "read", container_only: !authenticated, allowed_actions: [] },
    repository: { repository: { id: "cccccccc-cccc-4ccc-8ccc-cccccccccccc", organization_id: "bbbbbbbb-bbbb-4bbb-8bbb-bbbbbbbbbbbb", name: "workflow", display_name: "Workflow", visibility: "public", contribution_policy: "authenticated" }, effective_permission: authenticated ? "triage" : "read", allowed_actions },
  };
}
