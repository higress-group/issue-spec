import type { CSSProperties, ReactNode } from "react";
import { AlertTriangle, ArrowRight, Check, CheckCircle2, Circle, CircleDashed, CircleDot, Clock3, ExternalLink, ShieldAlert, Sparkles } from "lucide-react";
import { Link } from "react-router-dom";
import { useTranslation } from "react-i18next";
import { anomalyCopy, type Artifact, type BoardPageModel, type ChangeCardModel, type ChangeLifecycle, type ChangeStage, type Progress } from "./types";
import { CodeChangeIndicator } from "../changes/relationships";

type ArtifactStage = Exclude<ChangeStage, "unknown">;
const stageOrder: ArtifactStage[] = ["proposal", "design", "implement"];
function lifecycleIcon(lifecycle: ChangeLifecycle) {
  if (lifecycle === "blocked") return <ShieldAlert aria-hidden="true" />;
  if (lifecycle === "completed") return <CheckCircle2 aria-hidden="true" />;
  if (lifecycle === "closed") return <Check aria-hidden="true" />;
  return <CircleDot aria-hidden="true" />;
}

export function LifecycleBadge({ lifecycle }: { lifecycle: ChangeLifecycle }) {
  const { t } = useTranslation();
  return <span className={`board-lifecycle lifecycle-${lifecycle}`}>{lifecycleIcon(lifecycle)}{t(`changes.lifecycle.${lifecycle}`)}</span>;
}

function ArtifactNode({ stage, artifact, current, owner, repository }: { stage: ArtifactStage; artifact?: Artifact; current: boolean; owner: string; repository: string }) {
  const { t } = useTranslation();
  const stageLabel = t(`changes.stage.${stage}`);
  const artifactState = artifact?.state === "open" ? t("issues.detail.stateOpen") : artifact?.state === "closed" ? t("issues.detail.stateClosed") : artifact?.state;
  const state = !artifact ? "missing" : artifact.valid ? "valid" : "invalid";
  const icon = state === "missing" ? <CircleDashed aria-hidden="true" /> : state === "invalid" ? <AlertTriangle aria-hidden="true" /> : artifact?.state === "closed" ? <CheckCircle2 aria-hidden="true" /> : <Circle aria-hidden="true" />;
  const content = <>
    <span className="pipeline-node-icon">{icon}</span>
    <span className="pipeline-node-copy"><strong>{stageLabel}</strong><small>{artifact ? t("changes.card.issueArtifact", { number: artifact.number, state: artifactState }) : t("changes.card.artifactMissing")}</small></span>
    {artifact ? <ExternalLink className="pipeline-external" aria-hidden="true" /> : null}
  </>;
  const issue = artifact ? `/${encodeURIComponent(owner)}/${encodeURIComponent(repository)}/issues/${artifact.number}` : "";
  return <div className={`pipeline-node ${state} ${current ? "current" : ""}`} data-stage={stage}>
    {artifact ? <Link to={issue} aria-label={t("changes.card.artifactAria", { stage: stageLabel, number: artifact.number, validity: t(artifact.valid ? "changes.card.valid" : "changes.card.invalid") })}>{content}</Link> : <div aria-label={t("changes.card.artifactMissingAria", { stage: stageLabel })}>{content}</div>}
    {artifact && !artifact.valid ? <span className="pipeline-marker">{t("changes.card.marker", { version: artifact.marker_version })}</span> : null}
  </div>;
}

export function StagePipeline({ card, owner, expanded = false }: { card: ChangeCardModel; owner: string; expanded?: boolean }) {
  const { t } = useTranslation();
  return <div className={`change-pipeline ${expanded ? "expanded" : ""}`} aria-label={t("changes.card.pipelineAria", { stage: t(`changes.stage.${card.current_stage}`) })}>
    {stageOrder.map((stage, index) => <div className="pipeline-step" key={stage}>
      <ArtifactNode stage={stage} artifact={card.artifacts[stage]} current={card.current_stage === stage} owner={owner} repository={card.repository.name} />
      {index < stageOrder.length - 1 ? <ArrowRight className="pipeline-arrow" aria-hidden="true" /> : null}
    </div>)}
  </div>;
}

function progressPercent(progress: Progress) {
  return progress.total ? Math.round((progress.completed / progress.total) * 100) : 0;
}

export function ProgressMeter({ label, progress }: { label: string; progress: Progress }) {
  const { t } = useTranslation();
  const percent = progressPercent(progress);
  const style = { "--progress": `${percent}%` } as CSSProperties;
  return <div className="progress-meter">
    <div className="progress-heading"><span>{label}</span><strong>{progress.completed}/{progress.total}</strong></div>
    <div className="progress-track" role="progressbar" aria-label={t("changes.card.progressAria", { label, completed: progress.completed, total: progress.total })} aria-valuemin={0} aria-valuemax={progress.total} aria-valuenow={progress.completed} style={style}><span /></div>
    <div className="progress-legend">
      {progress.in_progress ? <span><Clock3 aria-hidden="true" />{t("changes.card.moving", { count: progress.in_progress })}</span> : null}
      {progress.blocked ? <span className="blocked"><ShieldAlert aria-hidden="true" />{t("changes.card.blocked", { count: progress.blocked })}</span> : null}
      {progress.pending ? <span>{t("changes.card.pending", { count: progress.pending })}</span> : null}
      {!progress.total ? <span>{t("changes.card.noRecords")}</span> : null}
    </div>
  </div>;
}

export function AnomalyList({ codes, compact = false }: { codes: string[]; compact?: boolean }) {
  const { t } = useTranslation();
  if (!codes.length) return null;
  return <div className={`anomaly-list ${compact ? "compact" : ""}`} aria-label={t("changes.card.anomaliesAria")}>
    {codes.map((code) => {
      const fallback = anomalyCopy(code);
      const known = code in anomalyCopyCatalog;
      const key = known ? code : "fallback";
      const copy = { label: t(`changes.anomalies.${key}.label`, { defaultValue: fallback.label }), description: t(`changes.anomalies.${key}.description`, { defaultValue: fallback.description }) };
      return <div className="anomaly-chip" key={code} title={copy.description}>
        <AlertTriangle aria-hidden="true" /><span><strong>{copy.label}</strong><code>{code}</code>{compact ? null : <small>{copy.description}</small>}</span>
      </div>;
    })}
  </div>;
}

const anomalyCopyCatalog = { duplicate_artifact_type: true, marker_label_mismatch: true, missing_required_links: true, unsupported_marker_version: true, implement_missing_predecessor: true, orphan_typed_artifact: true, malformed_issue_marker: true, code_change_binding_mismatch: true };

function formattedDate(value: string, language: string) {
  return new Intl.DateTimeFormat(language, { month: "short", day: "numeric", year: "numeric" }).format(new Date(value));
}

export function ChangeCard({ card, owner }: { card: ChangeCardModel; owner: string }) {
  const { t, i18n } = useTranslation();
  const detail = `/${encodeURIComponent(owner)}/${encodeURIComponent(card.repository.name)}/changes/${encodeURIComponent(card.change_key)}`;
  return <article className={`board-card card-${card.lifecycle}`}>
    <header className="board-card-header">
      <div><span className="board-repository">{card.repository.display_name || card.repository.name}</span><LifecycleBadge lifecycle={card.lifecycle} /></div>
      <time dateTime={card.updated_at}>{t("changes.card.updated", { date: formattedDate(card.updated_at, i18n.resolvedLanguage ?? i18n.language) })}</time>
    </header>
    <div className="board-card-title"><div><Link to={detail}>{card.title}</Link><code>{card.change_key}</code></div><span className="current-stage">{t("changes.card.current", { stage: t(`changes.stage.${card.current_stage}`) })}</span></div>
    <StagePipeline card={card} owner={owner} />
    <CodeChangeIndicator relationships={card.code_changes} />
    <div className="board-progress-grid"><ProgressMeter label={t("changes.card.tasks")} progress={card.tasks} /><ProgressMeter label={t("changes.card.processes")} progress={card.processes} /></div>
    <AnomalyList codes={card.anomalies} compact />
    <footer><Link className="board-open-link" to={detail}>{t("changes.card.openDetail")} <ArrowRight aria-hidden="true" /></Link></footer>
  </article>;
}

export function BoardSummary({ page }: { page: BoardPageModel }) {
  const { t } = useTranslation();
  const items = [
    { label: t("changes.card.visibleChanges"), value: page.counts.total, icon: <Sparkles aria-hidden="true" /> },
    { label: t("changes.lifecycle.active"), value: page.counts.active, icon: <CircleDot aria-hidden="true" /> },
    { label: t("changes.lifecycle.blocked"), value: page.counts.blocked, icon: <ShieldAlert aria-hidden="true" /> },
    { label: t("changes.lifecycle.completed"), value: page.counts.completed, icon: <CheckCircle2 aria-hidden="true" /> },
  ];
  return <section className="board-summary" aria-label={t("changes.card.summaryAria")}>{items.map((item) => <div key={item.label}>{item.icon}<span><strong>{item.value}</strong><small>{item.label}</small></span></div>)}</section>;
}

export function DiagnosticSummary({ diagnostics }: { diagnostics: BoardPageModel["diagnostics"] }) {
  const { t } = useTranslation();
  if (!diagnostics.length) return null;
  return <section className="diagnostic-summary" aria-labelledby="diagnostic-heading"><div><AlertTriangle aria-hidden="true" /><span><strong id="diagnostic-heading">{t("changes.card.diagnostics")}</strong><small>{t("changes.card.diagnosticsDescription")}</small></span></div><ul>{diagnostics.map(({ code, count }) => <li key={code}><span>{t(`changes.anomalies.${code in anomalyCopyCatalog ? code : "fallback"}.label`, { defaultValue: anomalyCopy(code).label })}</span><code>{code}</code><strong>{count}</strong></li>)}</ul></section>;
}

export function BoardState({ children, kind = "loading" }: { children: ReactNode; kind?: "loading" | "empty" | "safe" | "error" }) {
  const icon = kind === "loading" ? <span className="board-loader" aria-hidden="true" /> : kind === "safe" ? <ShieldAlert aria-hidden="true" /> : kind === "error" ? <AlertTriangle aria-hidden="true" /> : <CircleDashed aria-hidden="true" />;
  return <div className={`board-state state-${kind}`} role={kind === "loading" ? "status" : kind === "error" ? "alert" : undefined}>{icon}<div>{children}</div></div>;
}
