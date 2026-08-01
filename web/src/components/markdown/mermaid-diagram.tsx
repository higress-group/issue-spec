import DOMPurify from "dompurify";
import { Maximize2, Minus, Plus, RotateCcw, X } from "lucide-react";
import { useEffect, useId, useRef, useState, type PointerEvent as ReactPointerEvent } from "react";
import { useTranslation } from "react-i18next";

type MermaidState =
  | { source: string; status: "loading" }
  | { source: string; status: "ready"; dataUrl: string }
  | { source: string; status: "error" };

let diagramSequence = 0;
let mermaidPromise: Promise<(typeof import("mermaid"))["default"]> | undefined;
const minimumZoom = 50;
const maximumZoom = 300;
const zoomStep = 25;

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

type PanOrigin = {
  pointerId: number;
  x: number;
  y: number;
  scrollLeft: number;
  scrollTop: number;
};

function MermaidDiagramViewer({ dataUrl }: { dataUrl: string }) {
  const { t } = useTranslation();
  const titleId = useId();
  const dialogRef = useRef<HTMLDialogElement>(null);
  const canvasRef = useRef<HTMLDivElement>(null);
  const panOrigin = useRef<PanOrigin | null>(null);
  const [open, setOpen] = useState(false);
  const [zoom, setZoom] = useState(100);
  const [panning, setPanning] = useState(false);

  useEffect(() => {
    const dialog = dialogRef.current;
    if (!dialog) return;
    if (open && !dialog.open) {
      if (typeof dialog.showModal === "function") dialog.showModal();
      else dialog.open = true;
    } else if (!open && dialog.open) {
      if (typeof dialog.close === "function") dialog.close();
      else dialog.open = false;
    }
  }, [open]);

  const openViewer = () => {
    const canvas = canvasRef.current;
    if (canvas) {
      canvas.scrollLeft = 0;
      canvas.scrollTop = 0;
    }
    setZoom(100);
    setOpen(true);
  };
  const closeViewer = () => {
    const dialog = dialogRef.current;
    if (dialog?.open) {
      if (typeof dialog.close === "function") dialog.close();
      else dialog.open = false;
    }
    setOpen(false);
  };
  const changeZoom = (next: number) => setZoom(Math.max(minimumZoom, Math.min(maximumZoom, next)));
  const startPan = (event: ReactPointerEvent<HTMLDivElement>) => {
    const canvas = canvasRef.current;
    if (!canvas || event.button !== 0 || (canvas.scrollWidth <= canvas.clientWidth && canvas.scrollHeight <= canvas.clientHeight)) return;
    panOrigin.current = {
      pointerId: event.pointerId,
      x: event.clientX,
      y: event.clientY,
      scrollLeft: canvas.scrollLeft,
      scrollTop: canvas.scrollTop,
    };
    canvas.setPointerCapture?.(event.pointerId);
    setPanning(true);
  };
  const continuePan = (event: ReactPointerEvent<HTMLDivElement>) => {
    const canvas = canvasRef.current;
    const origin = panOrigin.current;
    if (!canvas || !origin || origin.pointerId !== event.pointerId) return;
    canvas.scrollLeft = origin.scrollLeft - (event.clientX - origin.x);
    canvas.scrollTop = origin.scrollTop - (event.clientY - origin.y);
  };
  const stopPan = (event: ReactPointerEvent<HTMLDivElement>) => {
    const canvas = canvasRef.current;
    const origin = panOrigin.current;
    if (!origin || origin.pointerId !== event.pointerId) return;
    if (canvas?.hasPointerCapture?.(event.pointerId)) canvas.releasePointerCapture(event.pointerId);
    panOrigin.current = null;
    setPanning(false);
  };

  return <>
    <button type="button" className="mermaid-diagram-trigger" aria-label={t("markdown.diagramViewer.open")} onClick={openViewer}>
      <img src={dataUrl} alt={t("markdown.mermaidDiagram")} />
      <span className="mermaid-diagram-expand" aria-hidden="true"><Maximize2 />{t("markdown.diagramViewer.expand")}</span>
    </button>
    <dialog
      ref={dialogRef}
      className="mermaid-diagram-viewer"
      aria-labelledby={titleId}
      onCancel={(event) => {
        event.preventDefault();
        closeViewer();
      }}
      onClose={() => setOpen(false)}
      onClick={(event) => { if (event.target === dialogRef.current) closeViewer(); }}
    >
      <section className="mermaid-diagram-viewer-shell">
        <header className="mermaid-diagram-viewer-header">
          <h2 id={titleId} className="mermaid-diagram-viewer-title">{t("markdown.diagramViewer.title")}</h2>
          <div className="mermaid-diagram-viewer-controls" role="toolbar" aria-label={t("markdown.diagramViewer.controls")}>
            <button type="button" aria-label={t("markdown.diagramViewer.zoomOut")} disabled={zoom === minimumZoom} onClick={() => changeZoom(zoom - zoomStep)}><Minus aria-hidden="true" /></button>
            <output aria-live="polite">{zoom}%</output>
            <button type="button" aria-label={t("markdown.diagramViewer.zoomIn")} disabled={zoom === maximumZoom} onClick={() => changeZoom(zoom + zoomStep)}><Plus aria-hidden="true" /></button>
            <button type="button" aria-label={t("markdown.diagramViewer.reset")} disabled={zoom === 100} onClick={() => changeZoom(100)}><RotateCcw aria-hidden="true" /></button>
            <button type="button" aria-label={t("markdown.diagramViewer.close")} onClick={closeViewer}><X aria-hidden="true" /></button>
          </div>
        </header>
        <div
          ref={canvasRef}
          className={`mermaid-diagram-viewer-canvas${panning ? " is-panning" : ""}`}
          tabIndex={0}
          aria-label={t("markdown.diagramViewer.canvas")}
          onPointerDown={startPan}
          onPointerMove={continuePan}
          onPointerUp={stopPan}
          onPointerCancel={stopPan}
        >
          {open ? <img
            className="mermaid-diagram-viewer-image"
            src={dataUrl}
            alt={t("markdown.mermaidDiagram")}
            draggable={false}
            style={{ width: `${zoom}%` }}
          /> : null}
        </div>
      </section>
    </dialog>
  </>;
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
    <MermaidDiagramViewer dataUrl={current.dataUrl} />
  </div>;
}
