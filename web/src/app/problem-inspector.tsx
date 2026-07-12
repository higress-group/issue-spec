import { createContext, useCallback, useContext, useEffect, useMemo, useRef, useState, type ReactNode } from "react";
import { AlertTriangle, CheckCircle2, Copy, X } from "lucide-react";
import { ApiProblem, isApiProblem } from "../lib/api/client";

type InspectorState = {
  problem?: ApiProblem;
  draft?: string;
  note?: string;
};

type InspectorContextValue = {
  state: InspectorState;
  open: boolean;
  report: (error: unknown, draft?: unknown) => void;
  note: (message: string) => void;
  clear: () => void;
  openInspector: () => void;
  closeInspector: () => void;
};

const InspectorContext = createContext<InspectorContextValue | undefined>(undefined);

export function InspectorProvider({ children }: { children: ReactNode }) {
  const [state, setState] = useState<InspectorState>({ note: "No request problems in this session." });
  const [open, setOpen] = useState(false);
  const report = useCallback((error: unknown, draft?: unknown) => {
    const problem = isApiProblem(error)
      ? error
      : new ApiProblem({ status: 0, title: "Client error", code: "client_error", detail: error instanceof Error ? error.message : "Unexpected error" });
    setState({ problem, draft: draft === undefined ? undefined : JSON.stringify(draft, null, 2) });
    setOpen(true);
  }, []);
  const value = useMemo<InspectorContextValue>(() => ({
    state, open,
    report,
    note: (message) => setState({ note: message }),
    clear: () => setState({ note: "Inspector cleared." }),
    openInspector: () => setOpen(true),
    closeInspector: () => setOpen(false),
  }), [open, report, state]);
  return <InspectorContext.Provider value={value}>{children}</InspectorContext.Provider>;
}

export function useInspector() {
  const value = useContext(InspectorContext);
  if (!value) throw new Error("useInspector must be used inside InspectorProvider");
  return value;
}

export function ProblemInspector({ identity, permission }: { identity?: string; permission?: string }) {
  const { state, open, clear, closeInspector } = useInspector();
  const closeRef = useRef<HTMLButtonElement>(null);
  useEffect(() => { if (open && window.matchMedia("(max-width: 1180px)").matches) closeRef.current?.focus(); }, [open]);
  const close = () => {
    closeInspector();
    document.getElementById("inspector-toggle")?.focus();
  };
  const problem = state.problem?.problem;
  const copyDraft = () => {
    if (state.draft) void navigator.clipboard.writeText(state.draft);
  };
  return (
    <aside id="request-inspector" className={`inspector ${open ? "open" : "closed"}`} aria-label="Request inspector">
      <div className="inspector-heading">
        <div>
          <span className="eyebrow">Right now</span>
          <h2>Inspector</h2>
        </div>
        <button ref={closeRef} className="icon-button inspector-close" type="button" onClick={close} aria-label="Close inspector"><X size={17} /></button>
      </div>
      <dl className="context-list">
        <div><dt>Identity</dt><dd>{identity ?? "Anonymous"}</dd></div>
        <div><dt>Permission</dt><dd>{permission ?? "—"}</dd></div>
      </dl>
      {problem ? (
        <section className="inspector-problem" aria-live="polite">
          <div className="status-line danger"><AlertTriangle size={17} /><strong>{problem.code}</strong></div>
          <p>{problem.detail || problem.title}</p>
          <dl className="context-list compact">
            <div><dt>Status</dt><dd>{problem.status || "client"}</dd></div>
            <div><dt>Request ID</dt><dd className="mono break-word">{problem.request_id ?? "not supplied"}</dd></div>
          </dl>
          {problem.code === "version_conflict" && state.draft ? (
            <div className="conflict-card">
              <strong>Your draft is preserved</strong>
              <p>Reload the latest resource, then reapply or copy this draft.</p>
              <button className="button secondary small" type="button" onClick={copyDraft}><Copy size={15} /> Copy draft</button>
            </div>
          ) : null}
        </section>
      ) : (
        <div className="inspector-ok"><CheckCircle2 size={18} /><p>{state.note}</p></div>
      )}
      <div className="inspector-footnote">
        <span className="stage-dot teal" />
        <p>Request IDs and conflicts stay here so the main workspace remains calm.</p>
      </div>
      {problem ? <button className="button secondary small inspector-clear" type="button" onClick={clear}>Clear request problem</button> : null}
    </aside>
  );
}
