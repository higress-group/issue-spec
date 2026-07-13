import { useEffect, useRef, type InputHTMLAttributes, type ReactNode, type SelectHTMLAttributes } from "react";
import { AlertCircle, Check, Clipboard, LoaderCircle, X } from "lucide-react";
import { isApiProblem } from "../lib/api/client";
import { useTranslation } from "react-i18next";

export function PageHeader({ eyebrow, title, description, actions }: { eyebrow?: string; title: string; description?: string; actions?: ReactNode }) {
  return <header className="page-header">
    <div>{eyebrow ? <span className="eyebrow">{eyebrow}</span> : null}<h1>{title}</h1>{description ? <p>{description}</p> : null}</div>
    {actions ? <div className="page-actions">{actions}</div> : null}
  </header>;
}

export function Panel({ title, description, children, className = "" }: { title?: string; description?: string; children: ReactNode; className?: string }) {
  return <section className={`panel ${className}`.trim()}>{title ? <div className="panel-heading"><h2>{title}</h2>{description ? <p>{description}</p> : null}</div> : null}{children}</section>;
}

export function Field({ label, hint, error, children }: { label: string; hint?: string; error?: string; children: ReactNode }) {
  return <label className="field"><span>{label}</span>{children}{hint ? <small>{hint}</small> : null}{error ? <small className="field-error">{error}</small> : null}</label>;
}

export function TextInput(props: InputHTMLAttributes<HTMLInputElement>) {
  return <input className="input" {...props} />;
}

export function SelectInput(props: SelectHTMLAttributes<HTMLSelectElement>) {
  return <select className="input" {...props} />;
}

export function Loading({ label = "Loading workspace" }: { label?: string }) {
  const { t } = useTranslation();
  return <div className="state-card" role="status"><LoaderCircle className="spin" size={20} /><p>{label === "Loading workspace" ? t("ui.loadingWorkspace") : label}</p></div>;
}

export function EmptyState({ title, description, action }: { title: string; description: string; action?: ReactNode }) {
  return <div className="empty-state"><span className="empty-mark">∴</span><h2>{title}</h2><p>{description}</p>{action}</div>;
}

export function ErrorNotice({ error }: { error: unknown }) {
  const { t } = useTranslation();
  const message = isApiProblem(error) ? error.problem.detail || error.problem.title : error instanceof Error ? error.message : t("ui.unexpectedError");
  return <div className="notice danger" role="alert"><AlertCircle size={18} /><p>{message}</p></div>;
}

export function StatusBadge({ tone = "neutral", children }: { tone?: "neutral" | "teal" | "purple" | "coral"; children: ReactNode }) {
  return <span className={`status-badge ${tone}`}>{children}</span>;
}

export function SecretDialog({ secret, title, onClose }: { secret: string; title: string; onClose: () => void }) {
  const { t } = useTranslation();
  const closeRef = useRef<HTMLButtonElement>(null);
  useEffect(() => {
    closeRef.current?.focus();
    const onKey = (event: KeyboardEvent) => { if (event.key === "Escape") onClose(); };
    window.addEventListener("keydown", onKey);
    return () => window.removeEventListener("keydown", onKey);
  }, [onClose]);
  const copy = () => void navigator.clipboard.writeText(secret);
  return <div className="dialog-backdrop" role="presentation">
    <section className="dialog" role="dialog" aria-modal="true" aria-labelledby="secret-title">
      <button ref={closeRef} className="icon-button dialog-close" type="button" onClick={onClose} aria-label={t("ui.closeSecret")}><X size={18} /></button>
      <span className="eyebrow coral-text">{t("ui.shownOnce")}</span>
      <h2 id="secret-title">{title}</h2>
      <p>{t("ui.secretHelp")}</p>
      <code className="secret-value">{secret}</code>
      <div className="dialog-actions"><button className="button primary" type="button" onClick={copy}><Clipboard size={16} /> {t("ui.copyCredential")}</button><button className="button secondary" type="button" onClick={onClose}><Check size={16} /> {t("ui.savedCredential")}</button></div>
    </section>
  </div>;
}
