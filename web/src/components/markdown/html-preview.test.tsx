import { fireEvent, screen, waitFor } from "@testing-library/react";
import userEvent from "@testing-library/user-event";
import { useState } from "react";
import { describe, expect, it, vi } from "vitest";
import { renderApp } from "../../../tests/render";
import { parsePreviewAnswerMessage } from "./html-preview-message";
import { createHtmlPreviewActivity, iframeAllow, type HtmlPreviewContext } from "./html-preview";
import { MarkdownView } from "./markdown-view";

const previewSource = (id = "design-review", body = "<!doctype html><button>Interactive</button>", metadata = "") =>
  `\`\`\`html-preview id=${id} version=1 title="${id}" height=999 ${metadata}\n${body}\n\`\`\``;

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

  it("is inert by default and uses the exact iframe boundary through Run, Reload, Stop, and collapse", async () => {
    const { context, previewURL } = previewContext();
    renderApp(<MarkdownView source={previewSource()} previewContext={context} />);
    expect(screen.queryByTitle("design-review")).not.toBeInTheDocument();
    expect(previewURL).not.toHaveBeenCalled();

    const iframe = await openAndRun("design-review");
    expect(previewURL).toHaveBeenCalledTimes(1);
    expect(previewURL.mock.calls[0][0]).toBe("design-review");
    expect(previewURL.mock.calls[0][1]).toMatch(/^[0-9a-f]{64}$/);
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
    expect(screen.queryByTitle("third")).not.toBeInTheDocument();

    const first = screen.getByTitle("first").closest(".html-preview")!;
    await userEvent.setup().click(first.querySelector("button")!);
    await userEvent.setup().click(screen.getByRole("button", { name: "Run" }));
    expect(await screen.findByTitle("third")).toBeInTheDocument();
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
