import { fireEvent, screen, waitFor } from "@testing-library/react";
import userEvent from "@testing-library/user-event";
import { useState } from "react";
import { afterEach, describe, expect, it, vi } from "vitest";
import { renderApp } from "../../../tests/render";
import { parsePreviewAnswerMessage, previewAnswerRequest } from "./html-preview-message";
import { createHtmlPreviewActivity, iframeAllow, type HtmlPreviewContext } from "./html-preview";
import { MarkdownView } from "./markdown-view";

const previewSource = (id = "design-review", body = "<!doctype html><button>Interactive</button>", metadata = "") =>
  `\`\`\`html-preview id=${id} version=1 title="${id}" height=999 ${metadata}\n${body}\n\`\`\``;
const expectedPreviewDigest = "81f1af6fe33563d5441441fd0867264c382ca4d0d966ad386e03d97f3d120e59";
const unicodePreviewBody = "<!doctype html><p>沙箱评审确保脚本隔离；这是一个跨越 SHA-256 分块边界的中文回归负载。安全边界保持不变。</p>";
const expectedUnicodePreviewDigest = "55d412f209a3588a1b33c0d0970bccb23a9e48a0e0c157a54f4d588df5c6aef0";

afterEach(() => {
  vi.unstubAllGlobals();
  vi.restoreAllMocks();
});

function previewContext(activity = createHtmlPreviewActivity(), answersEnabled = true) {
  const previewURL = vi.fn((id: string, digest: string) => `/api/v1/preview/${id}?digest=${digest}`);
  const onAnswerIntent = vi.fn();
  const context: HtmlPreviewContext = {
    sourceKey: "issue:41",
    activity,
    answersEnabled,
    previewURL,
    onAnswerIntent,
  };
  return { context, previewURL, onAnswerIntent };
}

async function openAndRun(title: string) {
  await userEvent.setup().click(screen.getByRole("button", { name: new RegExp(title) }));
  await userEvent.setup().click(screen.getByRole("button", { name: "Run" }));
  return screen.findByTitle(title);
}

describe("sandboxed HTML preview", () => {
  it("keeps unsupported and malformed previews as readable source", () => {
    const unsupported = renderApp(<MarkdownView source={previewSource()} />);
    expect(screen.getByText("<!doctype html><button>Interactive</button>")).toBeInTheDocument();
    expect(unsupported.container.querySelector(".html-preview")).toBeNull();
    unsupported.unmount();

    const { context } = previewContext();
    renderApp(<MarkdownView source={previewSource("future", "<p>source fallback</p>").replace("version=1", "version=2")} previewContext={context} />);
    expect(screen.getByText("<p>source fallback</p>")).toBeInTheDocument();
    expect(screen.queryByRole("button", { name: /future/ })).not.toBeInTheDocument();
  });

  it("keeps preview offsets aligned after hidden issue-spec markers are removed", () => {
    const { context } = previewContext();
    renderApp(<MarkdownView
      source={`<!-- issue-spec:type=PROCESS id=PROCESS-005 version=1 -->\n\n${previewSource("typed-review")}`}
      previewContext={context}
    />);
    expect(screen.getByRole("button", { name: /typed-review/ })).toBeInTheDocument();
    expect(screen.queryByText("<!doctype html><button>Interactive</button>")).not.toBeInTheDocument();
  });

  it("preserves exact preview bytes and digest across shorter inner backtick fences", async () => {
    const marker = "<!-- issue-spec:type=PROCESS id=PROCESS-999 version=1 -->";
    const body = ["<!doctype html>", "```markdown", marker, "```", "<p>exact bytes</p>"].join("\n");
    const source = [`\`\`\`\`html-preview id=fence-review version=1 title="fence-review"`, body, "````"].join("\n");
    const { context, previewURL } = previewContext();
    renderApp(<MarkdownView source={source} previewContext={context} />);

    await openAndRun("fence-review");
    expect(previewURL).toHaveBeenCalledWith(
      "fence-review",
      "67409d850ebef6543b8bbba8523603e756e82e087fe5623559949d5be29a8fcb",
    );
  });

  it("is inert by default and uses the exact iframe boundary through Run, Reload, Stop, and collapse", async () => {
    const { context, previewURL } = previewContext();
    renderApp(<MarkdownView source={previewSource()} previewContext={context} />);
    expect(screen.queryByTitle("design-review")).not.toBeInTheDocument();
    expect(previewURL).not.toHaveBeenCalled();

    const iframe = await openAndRun("design-review");
    expect(previewURL).toHaveBeenCalledTimes(1);
    expect(previewURL.mock.calls[0][0]).toBe("design-review");
    expect(previewURL.mock.calls[0][1]).toBe(expectedPreviewDigest);
    expect(iframe).toHaveAttribute("height", "720");
    expect(iframe).toHaveAttribute("sandbox", "allow-scripts");
    expect(iframe).toHaveAttribute("referrerpolicy", "no-referrer");
    expect(iframe).toHaveAttribute("allow", iframeAllow);
    expect(iframe).not.toHaveAttribute("srcdoc");
    expect(iframe.getAttribute("sandbox")).not.toContain("allow-same-origin");
    expect(iframe.getAttribute("sandbox")).not.toContain("allow-forms");
    expect(iframe.getAttribute("sandbox")).not.toContain("allow-popups");

    await userEvent.setup().click(screen.getByRole("button", { name: "Reload" }));
    const reloaded = await screen.findByTitle("design-review");
    expect(reloaded).not.toBe(iframe);
    await userEvent.setup().click(screen.getByRole("button", { name: "Stop" }));
    expect(screen.queryByTitle("design-review")).not.toBeInTheDocument();

    await userEvent.setup().click(screen.getByRole("button", { name: "Run" }));
    await screen.findByTitle("design-review");
    await userEvent.setup().click(screen.getByRole("button", { name: /design-review/ }));
    expect(screen.queryByTitle("design-review")).not.toBeInTheDocument();
  });

  it("renders only the first preview by default without overriding a manual collapse", async () => {
    const { context, previewURL } = previewContext();
    const source = [previewSource("first"), previewSource("second")].join("\n\n");
    const view = renderApp(<MarkdownView source={source} previewContext={context} renderFirstPreview={false} />);
    const first = screen.getByRole("button", { name: /first/ });
    const second = screen.getByRole("button", { name: /second/ });
    expect(first).toHaveAttribute("aria-expanded", "false");
    expect(second).toHaveAttribute("aria-expanded", "false");

    view.rerender(<MarkdownView source={source} previewContext={context} renderFirstPreview />);
    const expandedFirst = screen.getByRole("button", { name: /first/ });
    await waitFor(() => expect(expandedFirst).toHaveAttribute("aria-expanded", "true"));
    expect(screen.getByRole("button", { name: /second/ })).toHaveAttribute("aria-expanded", "false");
    expect(await screen.findByTitle("first")).toBeInTheDocument();
    expect(screen.getByRole("button", { name: "Reload" })).toBeVisible();
    expect(previewURL).toHaveBeenCalledTimes(1);
    expect(screen.queryByTitle("second")).not.toBeInTheDocument();

    await userEvent.setup().click(expandedFirst);
    expect(expandedFirst).toHaveAttribute("aria-expanded", "false");
    expect(screen.queryByTitle("first")).not.toBeInTheDocument();
    view.rerender(<MarkdownView source={source} previewContext={context} renderFirstPreview />);
    expect(screen.getByRole("button", { name: /first/ })).toHaveAttribute("aria-expanded", "false");
    expect(screen.queryByTitle("first")).not.toBeInTheDocument();
    expect(previewURL).toHaveBeenCalledTimes(1);
  });

  it("allows only two active frames and releases a slot on stop", async () => {
    const activity = createHtmlPreviewActivity(2);
    const { context } = previewContext(activity);
    const source = [previewSource("first"), previewSource("second"), previewSource("third")].join("\n\n");
    renderApp(<MarkdownView source={source} previewContext={context} />);
    await openAndRun("first");
    await openAndRun("second");
    await userEvent.setup().click(screen.getByRole("button", { name: /third/ }));
    await userEvent.setup().click(screen.getAllByRole("button", { name: "Run" }).at(-1)!);
    expect(await screen.findByRole("alert")).toHaveTextContent("Two previews are already active");
    expect(screen.getByRole("alert")).not.toHaveTextContent("could not start");
    expect(screen.queryByTitle("third")).not.toBeInTheDocument();

    const first = screen.getByTitle("first").closest(".html-preview")!;
    await userEvent.setup().click(first.querySelector("button")!);
    await userEvent.setup().click(screen.getByRole("button", { name: "Run" }));
    expect(await screen.findByTitle("third")).toBeInTheDocument();
  });

  it("uses exact SHA-256 and unique per-run correlation when browser crypto is unavailable", async () => {
    vi.stubGlobal("crypto", undefined);
    const { context, previewURL } = previewContext();
    expect(new TextEncoder().encode(`${unicodePreviewBody}\n`)).toHaveLength(146);
    renderApp(<MarkdownView source={previewSource("http-review", unicodePreviewBody)} previewContext={context} />);

    const iframe = await openAndRun("http-review") as HTMLIFrameElement;
    expect(previewURL).toHaveBeenLastCalledWith("http-review", expectedUnicodePreviewDigest);
    const firstPostMessage = vi.spyOn(iframe.contentWindow!, "postMessage");
    fireEvent.load(iframe);
    const firstNonce = (firstPostMessage.mock.calls.at(-1)?.[0] as { nonce: string }).nonce;
    expect(firstNonce).toMatch(/^[0-9a-f]{48}$/);

    await userEvent.setup().click(screen.getByRole("button", { name: "Reload" }));
    const reloaded = await screen.findByTitle("http-review") as HTMLIFrameElement;
    expect(reloaded).not.toBe(iframe);
    const secondPostMessage = vi.spyOn(reloaded.contentWindow!, "postMessage");
    fireEvent.load(reloaded);
    const secondNonce = (secondPostMessage.mock.calls.at(-1)?.[0] as { nonce: string }).nonce;
    expect(secondNonce).toMatch(/^[0-9a-f]{48}$/);
    expect(secondNonce).not.toBe(firstNonce);
  });

  it("releases a claimed slot if preview startup fails after the claim", async () => {
    vi.stubGlobal("crypto", undefined);
    const activity = { claim: vi.fn(() => true), release: vi.fn() };
    const { context } = previewContext(activity);
    const originalSetTimeout = globalThis.setTimeout;
    vi.spyOn(globalThis, "setTimeout").mockImplementation((handler, timeout, ...args) => {
      if (timeout === 10 * 60 * 1_000) throw new Error("timer unavailable");
      return originalSetTimeout(handler, timeout, ...args);
    });
    renderApp(<MarkdownView source={previewSource("timer-failure")} previewContext={context} />);
    await userEvent.setup().click(screen.getByRole("button", { name: /timer-failure/ }));
    await userEvent.setup().click(screen.getByRole("button", { name: "Run" }));

    expect(await screen.findByRole("alert")).toHaveTextContent("Preview could not start");
    expect(activity.claim).toHaveBeenCalledWith("issue:41:timer-failure");
    expect(activity.release).toHaveBeenCalledWith("issue:41:timer-failure");
    expect(screen.queryByTitle("timer-failure")).not.toBeInTheDocument();
  });

  it("reports startup failure separately from the active-preview limit", async () => {
    const { context } = previewContext();
    renderApp(<MarkdownView source={previewSource("startup")} previewContext={context} />);
    await userEvent.setup().click(screen.getByRole("button", { name: /startup/ }));
    vi.stubGlobal("TextEncoder", undefined);
    await userEvent.setup().click(screen.getByRole("button", { name: "Run" }));

    expect(await screen.findByRole("alert")).toHaveTextContent("Preview could not start");
    expect(screen.getByRole("alert")).not.toHaveTextContent("Two previews are already active");
    expect(screen.queryByTitle("startup")).not.toBeInTheDocument();
  });

  it("does not remount an unchanged running frame on parent rerender and tears down changed source", async () => {
    const { context } = previewContext();
    function Harness() {
      const [tick, setTick] = useState(0);
      const [body, setBody] = useState("one");
      return <><button type="button" onClick={() => setTick((value) => value + 1)}>Tick {tick}</button>
        <button type="button" onClick={() => setBody("two")}>Change source</button>
        <MarkdownView source={previewSource("stable", `<p>${body}</p>`)} previewContext={context} /></>;
    }
    renderApp(<Harness />);
    const iframe = await openAndRun("stable");
    await userEvent.setup().click(screen.getByRole("button", { name: "Tick 0" }));
    expect(screen.getByTitle("stable")).toBe(iframe);
    await userEvent.setup().click(screen.getByRole("button", { name: "Change source" }));
    await waitFor(() => expect(screen.queryByTitle("stable")).not.toBeInTheDocument());
  });

  it("sends only nonce and answer capability to the child after load", async () => {
    const { context } = previewContext(undefined, false);
    renderApp(<MarkdownView source={previewSource("init")} previewContext={context} />);
    const iframe = await openAndRun("init") as HTMLIFrameElement;
    const postMessage = vi.spyOn(iframe.contentWindow!, "postMessage");
    fireEvent.load(iframe);
    expect(postMessage).toHaveBeenCalledWith({
      version: 1,
      type: "issue-spec-preview-init",
      nonce: expect.stringMatching(/^[0-9a-f]{48}$/),
      interactive_question_answers: false,
    }, "*");
    expect(JSON.stringify(postMessage.mock.calls[0][0])).not.toContain("issue:41");
  });
});

describe("preview answer message validation", () => {
  const source = {} as MessageEventSource;
  const nonce = "abc123";
  const valid = {
    version: 1,
    nonce,
    question_id: "QUESTION-007",
    mode: "multiple",
    option_ids: ["safe", "fast"],
    custom: "",
  };
  const event = (data: unknown, overrides: Partial<Pick<MessageEvent, "origin" | "source">> = {}) => ({
    data,
    origin: "null",
    source,
    ...overrides,
  });

  it("accepts the exact bounded intent without trusted binding", () => {
    expect(parsePreviewAnswerMessage(event(valid), source, nonce)).toEqual({
      questionId: "QUESTION-007",
      mode: "multiple",
      optionIds: ["safe", "fast"],
      custom: "",
    });
  });

  it("budgets the complete escaped native request at the 16 KiB boundary", () => {
    const acceptedCustom = "\0".repeat(2_706) + "x".repeat(5);
    const accepted = parsePreviewAnswerMessage(event({ ...valid, option_ids: [], custom: acceptedCustom }), source, nonce);
    expect(accepted).not.toBeNull();
    expect(new TextEncoder().encode(JSON.stringify(previewAnswerRequest(accepted!, "0".repeat(64)))).byteLength).toBe(16 * 1_024);

    const rejectedCustom = acceptedCustom + "x";
    expect(parsePreviewAnswerMessage(event({ ...valid, option_ids: [], custom: rejectedCustom }), source, nonce)).toBeNull();
    expect(new TextEncoder().encode(JSON.stringify(previewAnswerRequest({
      questionId: "QUESTION-007",
      mode: "multiple",
      optionIds: [],
      custom: rejectedCustom,
    }, "0".repeat(64)))).byteLength).toBe(16 * 1_024 + 1);
  });

  it.each([
    ["another window", event(valid, { source: {} as MessageEventSource })],
    ["non-opaque origin", event(valid, { origin: "http://localhost" })],
    ["wrong nonce", event({ ...valid, nonce: "wrong" })],
    ["unknown field", event({ ...valid, owner: "acme" })],
    ["duplicate option", event({ ...valid, option_ids: ["safe", "safe"] })],
    ["invalid question", event({ ...valid, question_id: "question-7" })],
    ["single with many", event({ ...valid, mode: "single", option_ids: ["safe", "fast"] })],
    ["options plus custom", event({ ...valid, custom: "other" })],
    ["oversized custom", event({ ...valid, option_ids: [], custom: "x".repeat(4_097) })],
  ])("rejects %s", (_name, candidate) => {
    expect(parsePreviewAnswerMessage(candidate, source, nonce)).toBeNull();
  });
});
