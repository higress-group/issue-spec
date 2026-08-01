import { fireEvent, screen, waitFor } from "@testing-library/react";
import userEvent from "@testing-library/user-event";
import { useState } from "react";
import { afterEach, describe, expect, it, vi } from "vitest";
import { renderApp } from "../../../tests/render";
import { iframeAllow, type HtmlPreviewContext } from "./html-preview";
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

function previewContext() {
  const previewURL = vi.fn((id: string, digest: string) => `/api/v1/preview/${id}?digest=${digest}`);
  const context: HtmlPreviewContext = { previewURL };
  return { context, previewURL };
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
    expect(screen.queryByTitle("future")).not.toBeInTheDocument();
  });

  it("keeps preview offsets aligned after hidden issue-spec markers are removed", async () => {
    const { context } = previewContext();
    renderApp(<MarkdownView
      source={`<!-- issue-spec:type=PROCESS id=PROCESS-005 version=1 -->\n\n${previewSource("typed-review")}`}
      previewContext={context}
    />);
    expect(await screen.findByTitle("typed-review")).toBeInTheDocument();
    expect(screen.queryByText("<!doctype html><button>Interactive</button>")).not.toBeInTheDocument();
  });

  it("preserves exact preview bytes and digest across shorter inner backtick fences", async () => {
    const marker = "<!-- issue-spec:type=PROCESS id=PROCESS-999 version=1 -->";
    const body = ["<!doctype html>", "```markdown", marker, "```", "<p>exact bytes</p>"].join("\n");
    const source = [`\`\`\`\`html-preview id=fence-review version=1 title="fence-review"`, body, "````"].join("\n");
    const { context, previewURL } = previewContext();
    renderApp(<MarkdownView source={source} previewContext={context} />);

    await screen.findByTitle("fence-review");
    expect(previewURL).toHaveBeenCalledWith(
      "fence-review",
      "67409d850ebef6543b8bbba8523603e756e82e087fe5623559949d5be29a8fcb",
    );
  });

  it("renders every valid preview directly without wrapper controls or an active-frame limit", async () => {
    const { context, previewURL } = previewContext();
    const titles = ["first", "second", "third", "fourth", "fifth", "sixth", "seventh", "eighth", "ninth"];
    const source = titles.map((title) => previewSource(title)).join("\n\n");
    renderApp(<MarkdownView source={source} previewContext={context} />);

    const frames = await Promise.all(titles.map((title) => screen.findByTitle(title)));
    expect(frames).toHaveLength(titles.length);
    expect(previewURL).toHaveBeenCalledTimes(titles.length);
    expect(screen.queryByRole("button", { name: /Run|Reload|Stop/ })).not.toBeInTheDocument();
    expect(document.querySelector(".html-preview-body")).not.toBeInTheDocument();
    expect(document.querySelector(".html-preview-state")).not.toBeInTheDocument();
  });

  it("uses the exact iframe sandbox boundary on direct render", async () => {
    const { context, previewURL } = previewContext();
    renderApp(<MarkdownView source={previewSource()} previewContext={context} />);
    const iframe = await screen.findByTitle("design-review");

    expect(previewURL).toHaveBeenCalledWith("design-review", expectedPreviewDigest);
    expect(iframe).toHaveAttribute("height", "720");
    expect(iframe).toHaveAttribute("sandbox", "allow-scripts");
    expect(iframe).toHaveAttribute("referrerpolicy", "no-referrer");
    expect(iframe).toHaveAttribute("allow", iframeAllow);
    expect(iframe).not.toHaveAttribute("srcdoc");
    expect(iframe.getAttribute("sandbox")).not.toContain("allow-same-origin");
    expect(iframe.getAttribute("sandbox")).not.toContain("allow-forms");
    expect(iframe.getAttribute("sandbox")).not.toContain("allow-popups");
  });

  it("uses exact SHA-256 when browser crypto is unavailable", async () => {
    vi.stubGlobal("crypto", undefined);
    const { context, previewURL } = previewContext();
    expect(new TextEncoder().encode(`${unicodePreviewBody}\n`)).toHaveLength(146);
    renderApp(<MarkdownView source={previewSource("http-review", unicodePreviewBody)} previewContext={context} />);

    await screen.findByTitle("http-review");
    expect(previewURL).toHaveBeenCalledWith("http-review", expectedUnicodePreviewDigest);
  });

  it("does not remount an unchanged frame on parent rerender and replaces changed source", async () => {
    const { context, previewURL } = previewContext();
    function Harness() {
      const [tick, setTick] = useState(0);
      const [body, setBody] = useState("one");
      return <><button type="button" onClick={() => setTick((value) => value + 1)}>Tick {tick}</button>
        <button type="button" onClick={() => setBody("two")}>Change source</button>
        <MarkdownView source={previewSource("stable", `<p>${body}</p>`)} previewContext={context} /></>;
    }
    renderApp(<Harness />);
    const iframe = await screen.findByTitle("stable");
    await userEvent.setup().click(screen.getByRole("button", { name: "Tick 0" }));
    expect(screen.getByTitle("stable")).toBe(iframe);
    expect(previewURL).toHaveBeenCalledTimes(1);

    await userEvent.setup().click(screen.getByRole("button", { name: "Change source" }));
    await waitFor(() => expect(previewURL).toHaveBeenCalledTimes(2));
    expect(screen.getByTitle("stable")).not.toBe(iframe);
  });

  it("does not initialize an answer capability after load", async () => {
    const { context } = previewContext();
    renderApp(<MarkdownView source={previewSource("display-only")} previewContext={context} />);
    const iframe = await screen.findByTitle("display-only") as HTMLIFrameElement;
    const postMessage = vi.spyOn(iframe.contentWindow!, "postMessage");
    fireEvent.load(iframe);
    expect(postMessage).not.toHaveBeenCalled();
  });
});
