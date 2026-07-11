import type { CSSProperties, ReactNode } from "react";
import { AlertTriangle, ArrowRight, Check, CheckCircle2, Circle, CircleDashed, CircleDot, Clock3, ExternalLink, ShieldAlert, Sparkles } from "lucide-react";
import { Link } from "react-router-dom";
import { anomalyCopy, type Artifact, type BoardPageModel, type ChangeCardModel, type ChangeLifecycle, type ChangeStage, type Progress } from "./types";

type ArtifactStage = Exclude<ChangeStage, "unknown">;
const stageOrder: ArtifactStage[] = ["proposal", "design", "implement"];
const stageLabels: Record<ChangeStage, string> = { unknown: "Unknown", proposal: "Proposal", design: "Design", implement: "Implement" };
const lifecycleLabels: Record<ChangeLifecycle, string> = { active: "Active", blocked: "Blocked", completed: "Completed", closed: "Closed" };

function lifecycleIcon(lifecycle: ChangeLifecycle) {
  if (lifecycle === "blocked") return <ShieldAlert aria-hidden="true" />;
  if (lifecycle === "completed") return <CheckCircle2 aria-hidden="true" />;
  if (lifecycle === "closed") return <Check aria-hidden="true" />;
  return <CircleDot aria-hidden="true" />;
}

export function LifecycleBadge({ lifecycle }: { lifecycle: ChangeLifecycle }) {
  return <span className={`board-lifecycle lifecycle-${lifecycle}`}>{lifecycleIcon(lifecycle)}{lifecycleLabels[lifecycle]}</span>;
}

function ArtifactNode({ stage, artifact, current, owner, repository }: { stage: ArtifactStage; artifact?: Artifact; current: boolean; owner: string; repository: string }) {
  const state = !artifact ? "missing" : artifact.valid ? "valid" : "invalid";
  const icon = state === "missing" ? <CircleDashed aria-hidden="true" /> : state === "invalid" ? <AlertTriangle aria-hidden="true" /> : artifact?.state === "closed" ? <CheckCircle2 aria-hidden="true" /> : <Circle aria-hidden="true" />;
  const content = <>
    <span className="pipeline-node-icon">{icon}</span>
    <span className="pipeline-node-copy"><strong>{stageLabels[stage]}</strong><small>{artifact ? `Issue #${artifact.number} · ${artifact.state}` : "Artifact missing"}</small></span>
    {artifact ? <ExternalLink className="pipeline-external" aria-hidden="true" /> : null}
  </>;
  const issue = artifact ? `/${encodeURIComponent(owner)}/${encodeURIComponent(repository)}/issues/${artifact.number}` : "";
  return <div className={`pipeline-node ${state} ${current ? "current" : ""}`} data-stage={stage}>
    {artifact ? <Link to={issue} aria-label={`${stageLabels[stage]} artifact, issue ${artifact.number}, ${artifact.valid ? "valid" : "invalid"}`}>{content}</Link> : <div aria-label={`${stageLabels[stage]} artifact missing`}>{content}</div>}
    {artifact && !artifact.valid ? <span className="pipeline-marker">marker v{artifact.marker_version}</span> : null}
  </div>;
}

export function StagePipeline({ card, owner, expanded = false }: { card: ChangeCardModel; owner: string; expanded?: boolean }) {
  return <div className={`change-pipeline ${expanded ? "expanded" : ""}`} aria-label={`Change pipeline, current stage ${stageLabels[card.current_stage]}`}>
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
  const percent = progressPercent(progress);
  const style = { "--progress": `${percent}%` } as CSSProperties;
  return <div className="progress-meter">
    <div className="progress-heading"><span>{label}</span><strong>{progress.completed}/{progress.total}</strong></div>
    <div className="progress-track" role="progressbar" aria-label={`${label}: ${progress.completed} of ${progress.total} complete`} aria-valuemin={0} aria-valuemax={progress.total} aria-valuenow={progress.completed} style={style}><span /></div>
    <div className="progress-legend">
      {progress.in_progress ? <span><Clock3 aria-hidden="true" />{progress.in_progress} moving</span> : null}
      {progress.blocked ? <span className="blocked"><ShieldAlert aria-hidden="true" />{progress.blocked} blocked</span> : null}
      {progress.pending ? <span>{progress.pending} pending</span> : null}
      {!progress.total ? <span>No records yet</span> : null}
    </div>
  </div>;
}

export function AnomalyList({ codes, compact = false }: { codes: string[]; compact?: boolean }) {
  if (!codes.length) return null;
  return <div className={`anomaly-list ${compact ? "compact" : ""}`} aria-label="Projection anomalies">
    {codes.map((code) => {
      const copy = anomalyCopy(code);
      return <div className="anomaly-chip" key={code} title={copy.description}>
        <AlertTriangle aria-hidden="true" /><span><strong>{copy.label}</strong><code>{code}</code>{compact ? null : <small>{copy.description}</small>}</span>
      </div>;
    })}
  </div>;
}

function formattedDate(value: string) {
  return new Intl.DateTimeFormat(undefined, { month: "short", day: "numeric", year: "numeric" }).format(new Date(value));
}

export function ChangeCard({ card, owner }: { card: ChangeCardModel; owner: string }) {
  const detail = `/${encodeURIComponent(owner)}/${encodeURIComponent(card.repository.name)}/changes/${encodeURIComponent(card.change_key)}`;
  return <article className={`board-card card-${card.lifecycle}`}>
    <header className="board-card-header">
      <div><span className="board-repository">{card.repository.display_name || card.repository.name}</span><LifecycleBadge lifecycle={card.lifecycle} /></div>
      <time dateTime={card.updated_at}>Updated {formattedDate(card.updated_at)}</time>
    </header>
    <div className="board-card-title"><div><Link to={detail}>{card.title}</Link><code>{card.change_key}</code></div><span className="current-stage">Current · {stageLabels[card.current_stage]}</span></div>
    <StagePipeline card={card} owner={owner} />
    <div className="board-progress-grid"><ProgressMeter label="Tasks" progress={card.tasks} /><ProgressMeter label="Processes" progress={card.processes} /></div>
    <AnomalyList codes={card.anomalies} compact />
    <footer><Link className="board-open-link" to={detail}>Open change detail <ArrowRight aria-hidden="true" /></Link></footer>
  </article>;
}

export function BoardSummary({ page }: { page: BoardPageModel }) {
  const items = [
    { label: "Visible changes", value: page.counts.total, icon: <Sparkles aria-hidden="true" /> },
    { label: "Active", value: page.counts.active, icon: <CircleDot aria-hidden="true" /> },
    { label: "Blocked", value: page.counts.blocked, icon: <ShieldAlert aria-hidden="true" /> },
    { label: "Completed", value: page.counts.completed, icon: <CheckCircle2 aria-hidden="true" /> },
  ];
  return <section className="board-summary" aria-label="Change board summary">{items.map((item) => <div key={item.label}>{item.icon}<span><strong>{item.value}</strong><small>{item.label}</small></span></div>)}</section>;
}

export function DiagnosticSummary({ diagnostics }: { diagnostics: BoardPageModel["diagnostics"] }) {
  if (!diagnostics.length) return null;
  return <section className="diagnostic-summary" aria-labelledby="diagnostic-heading"><div><AlertTriangle aria-hidden="true" /><span><strong id="diagnostic-heading">Projection diagnostics</strong><small>Visible repositories reported structural workflow records that need attention.</small></span></div><ul>{diagnostics.map(({ code, count }) => <li key={code}><span>{anomalyCopy(code).label}</span><code>{code}</code><strong>{count}</strong></li>)}</ul></section>;
}

export function BoardState({ children, kind = "loading" }: { children: ReactNode; kind?: "loading" | "empty" | "safe" | "error" }) {
  const icon = kind === "loading" ? <span className="board-loader" aria-hidden="true" /> : kind === "safe" ? <ShieldAlert aria-hidden="true" /> : kind === "error" ? <AlertTriangle aria-hidden="true" /> : <CircleDashed aria-hidden="true" />;
  return <div className={`board-state state-${kind}`} role={kind === "loading" ? "status" : kind === "error" ? "alert" : undefined}>{icon}<div>{children}</div></div>;
}
