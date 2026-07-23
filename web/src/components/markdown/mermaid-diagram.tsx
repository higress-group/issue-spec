import DOMPurify from "dompurify";
import { useEffect, useState } from "react";
import { useTranslation } from "react-i18next";

type MermaidState =
  | { source: string; status: "loading" }
  | { source: string; status: "ready"; dataUrl: string }
  | { source: string; status: "error" };

let diagramSequence = 0;
let mermaidPromise: Promise<(typeof import("mermaid"))["default"]> | undefined;

function loadMermaid() {
  mermaidPromise ??= import("mermaid").then(({ default: mermaid }) => {
    mermaid.initialize({
      startOnLoad: false,
      securityLevel: "strict",
      suppressErrorRendering: true,
      theme: "neutral",
      htmlLabels: false,
    });
    return mermaid;
  });
  return mermaidPromise;
}

function sanitizedSvgDataUrl(svg: string) {
  const clean = DOMPurify.sanitize(svg, {
    USE_PROFILES: { svg: true, svgFilters: true },
    ADD_TAGS: ["style"],
    FORBID_TAGS: ["a", "embed", "foreignObject", "iframe", "object", "script"],
    FORBID_ATTR: ["href", "target", "xlink:href"],
  });
  return `data:image/svg+xml;charset=utf-8,${encodeURIComponent(clean)}`;
}

function removeTemporaryRenderNode(id: string) {
  document.getElementById(`d${id}`)?.remove();
}

export function MermaidDiagram({ source }: { source: string }) {
  const { t } = useTranslation();
  const [state, setState] = useState<MermaidState>({ source, status: "loading" });
  const current = state.source === source ? state : { source, status: "loading" } as const;

  useEffect(() => {
    const id = `issue-spec-mermaid-${++diagramSequence}`;
    let active = true;
    void loadMermaid()
      .then((mermaid) => mermaid.render(id, source))
      .then(({ svg }) => {
        if (active) setState({ source, status: "ready", dataUrl: sanitizedSvgDataUrl(svg) });
      })
      .catch(() => {
        removeTemporaryRenderNode(id);
        if (active) setState({ source, status: "error" });
      });
    return () => {
      active = false;
      removeTemporaryRenderNode(id);
    };
  }, [source]);

  if (current.status === "loading") {
    return <div className="mermaid-diagram mermaid-diagram-loading" role="status">{t("markdown.mermaidLoading")}</div>;
  }
  if (current.status === "error") {
    return <div className="mermaid-diagram-fallback">
      <p role="alert">{t("markdown.mermaidError")}</p>
      <pre tabIndex={0} aria-label={t("markdown.codeBlock")}><code className="language-mermaid">{source}</code></pre>
    </div>;
  }
  return <div className="mermaid-diagram" data-testid="mermaid-diagram">
    <img src={current.dataUrl} alt={t("markdown.mermaidDiagram")} />
  </div>;
}
