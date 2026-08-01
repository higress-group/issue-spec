import { fireEvent, screen, waitFor, within } from "@testing-library/react";
import { useState } from "react";
import userEvent from "@testing-library/user-event";
import { beforeEach, describe, expect, it, vi } from "vitest";
import { renderApp } from "../../../tests/render";
import { MarkdownView } from "./markdown-view";

const mermaid = vi.hoisted(() => ({
  initialize: vi.fn(),
  render: vi.fn(),
}));

vi.mock("mermaid", () => ({ default: mermaid }));

function decodedSvg(image: HTMLElement) {
  const source = image.getAttribute("src");
  expect(source).toMatch(/^data:image\/svg\+xml;charset=utf-8,/);
  return decodeURIComponent(source?.split(",", 2)[1] ?? "");
}

describe("Mermaid Markdown diagrams", () => {
  beforeEach(() => {
    mermaid.initialize.mockClear();
    mermaid.render.mockReset();
  });

  it("renders an exact mermaid fence with strict, non-interactive settings", async () => {
    mermaid.render.mockResolvedValue({
      svg: '<svg xmlns="http://www.w3.org/2000/svg"><text>Task builder</text></svg>',
    });
    const source = "flowchart LR\nA[Task builder] --> B[Worker]";
    const { container } = renderApp(<MarkdownView source={`\`\`\`mermaid\n${source}\n\`\`\``} />);

    const diagram = await screen.findByRole("img", { name: "Mermaid diagram" });
    expect(decodedSvg(diagram)).toContain("<text>Task builder</text>");
    expect(container.querySelector("pre")).toBeNull();
    expect(mermaid.initialize).toHaveBeenCalledWith(expect.objectContaining({
      htmlLabels: false,
      securityLevel: "strict",
      startOnLoad: false,
      suppressErrorRendering: true,
    }));
    expect(mermaid.render).toHaveBeenCalledWith(expect.stringMatching(/^issue-spec-mermaid-\d+$/), source);
  });

  it("sanitizes generated SVG before adding it to the page", async () => {
    mermaid.render.mockResolvedValue({
      svg: '<svg xmlns="http://www.w3.org/2000/svg"><script>alert(1)</script><foreignObject><img src="x" onerror="alert(2)"></foreignObject><a href="javascript:alert(3)"><text onclick="alert(4)">Safe label</text></a></svg>',
    });
    const { container } = renderApp(<MarkdownView source={"```mermaid\nflowchart LR\nA --> B\n```"} />);

    const diagram = await screen.findByRole("img", { name: "Mermaid diagram" });
    const svg = decodedSvg(diagram);
    expect(svg).toContain("Safe label");
    expect(svg).not.toMatch(/<script|<foreignObject|<a\b|<img|onclick|onerror|href=/);
    expect(container.querySelector(".mermaid-diagram script,.mermaid-diagram foreignObject,.mermaid-diagram a")).toBeNull();
  });

  it("opens a keyboard-accessible enlarged viewer with bounded zoom controls", async () => {
    mermaid.render.mockResolvedValue({
      svg: '<svg xmlns="http://www.w3.org/2000/svg"><text>Inspect me</text></svg>',
    });
    renderApp(<MarkdownView source={"```mermaid\nflowchart LR\nA --> B\n```"} />);
    const user = userEvent.setup();

    await user.click(await screen.findByRole("button", { name: "Open Mermaid diagram in enlarged view" }));

    const dialog = screen.getByRole("dialog", { name: "Mermaid diagram viewer" });
    expect(dialog).toHaveAttribute("open");
    const expanded = within(dialog).getByRole("img", { name: "Mermaid diagram" });
    expect(decodedSvg(expanded)).toContain("Inspect me");
    expect(expanded).toHaveStyle({ width: "100%" });

    await user.click(within(dialog).getByRole("button", { name: "Zoom in" }));
    expect(expanded).toHaveStyle({ width: "125%" });
    expect(within(dialog).getByText("125%")).toBeInTheDocument();
    await user.click(within(dialog).getByRole("button", { name: "Reset zoom" }));
    expect(expanded).toHaveStyle({ width: "100%" });

    fireEvent(dialog, new Event("cancel", { bubbles: false, cancelable: true }));
    await waitFor(() => expect(screen.queryByRole("dialog", { name: "Mermaid diagram viewer" })).not.toBeInTheDocument());
  });

  it("falls back to the original source when parsing fails", async () => {
    mermaid.render.mockRejectedValue(new Error("Parse error"));
    const source = "flowchart definitely-not-valid";
    const { container } = renderApp(<MarkdownView source={`\`\`\`mermaid\n${source}\n\`\`\``} />);

    expect(await screen.findByRole("alert")).toHaveTextContent("The source is shown instead");
    expect(container.querySelector("code.language-mermaid")).toHaveTextContent(source);
  });

  it("keeps similarly named languages as ordinary code", () => {
    const { container } = renderApp(<MarkdownView source={"```mermaid-example\nnot a diagram\n```"} />);

    expect(container.querySelector("code.language-mermaid-example")).toHaveTextContent("not a diagram");
    expect(screen.queryByTestId("mermaid-diagram")).not.toBeInTheDocument();
    expect(mermaid.render).not.toHaveBeenCalled();
  });

  it("does not rebuild a diagram when its parent refreshes unrelated state", async () => {
    mermaid.render.mockResolvedValue({
      svg: '<svg xmlns="http://www.w3.org/2000/svg"><text>Stable</text></svg>',
    });
    function RefreshingParent() {
      const [tick, setTick] = useState(0);
      return <><button type="button" onClick={() => setTick((value) => value + 1)}>Tick {tick}</button><MarkdownView source={"```mermaid\nflowchart LR\nA --> B\n```"} /></>;
    }
    renderApp(<RefreshingParent />);
    const initial = await screen.findByRole("img", { name: "Mermaid diagram" });

    await userEvent.setup().click(screen.getByRole("button", { name: "Tick 0" }));

    expect(screen.getByRole("img", { name: "Mermaid diagram" })).toBe(initial);
    expect(mermaid.render).toHaveBeenCalledTimes(1);
  });
});
