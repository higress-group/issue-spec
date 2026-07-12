import { useQuery } from "@tanstack/react-query";
import { ArrowLeft, GitBranch, GitPullRequest, Layers3, ShieldAlert } from "lucide-react";
import { Link, useParams } from "react-router-dom";
import { isApiProblem } from "../../lib/api/client";
import { boardApi } from "./api";
import { AnomalyList, BoardState, LifecycleBadge, ProgressMeter, StagePipeline } from "./components";
import { SafeBoardState } from "./board-page";
import { RepositoryGate, repositoryChangePath, type ActiveRepository } from "../issues/repository-context";
import { CodeChangeList } from "../changes/relationships";

function BoardDetail({ active }: { active: ActiveRepository }) {
  const { change = "" } = useParams();
  const orgId = active.organization.id;
  const repoId = active.repository.repository.id;
  const detail = useQuery({ queryKey: ["boards", orgId, repoId, "detail", change], queryFn: ({ signal }) => boardApi.change(orgId, repoId, change, signal), enabled: Boolean(orgId && repoId && change) });
  if (detail.isLoading) return <BoardState>Opening change projection…</BoardState>;
  if (isApiProblem(detail.error, "not_found") || isApiProblem(detail.error, "forbidden")) return <SafeBoardState />;
  if (detail.error || !detail.data) return <BoardState kind="error"><h1>Change unavailable</h1><p>The projection could not be loaded. Try again or inspect the request ID.</p></BoardState>;
  const card = detail.data;
  return <div className="board-page board-detail">
    <header className="detail-masthead"><Link className="board-back" to={repositoryChangePath(active)}><ArrowLeft aria-hidden="true" />{card.repository.display_name}</Link><div className="detail-title"><div><span className="board-eyebrow">Change detail</span><h1>{card.title}</h1><code>{card.change_key}</code></div><LifecycleBadge lifecycle={card.lifecycle} /></div><p>One permission-filtered view across the artifact chain and its execution records.</p></header>
    <section className="detail-panel pipeline-panel" aria-labelledby="pipeline-heading"><header><span><Layers3 aria-hidden="true" /><span><small>Artifact chain</small><h2 id="pipeline-heading">From intent to implementation</h2></span></span><strong>{card.current_stage} stage</strong></header><StagePipeline card={card} owner={active.organization.name} expanded /></section>
    <section className="detail-panel code-change-panel" aria-labelledby="code-changes-heading"><header><span><GitPullRequest aria-hidden="true" /><span><small>Linked delivery</small><h2 id="code-changes-heading">Code changes</h2></span></span><strong>{card.code_changes.length} active</strong></header><CodeChangeList relationships={card.code_changes} empty="No active code change is linked to the implementation issue." />{card.code_changes.some((relationship) => relationship.source_binding_match === "mismatched") ? <div className="binding-diagnostic" role="note"><ShieldAlert aria-hidden="true" /><span><strong>Source binding mismatch</strong><small>At least one relationship points at another provider repository. Verify the active binding before accepting delivery evidence.</small></span></div> : null}</section>
    <div className="detail-columns"><section className="detail-panel" aria-labelledby="delivery-heading"><header><span><GitBranch aria-hidden="true" /><span><small>Delivery pulse</small><h2 id="delivery-heading">Execution progress</h2></span></span></header><div className="detail-progress"><ProgressMeter label="Tasks" progress={card.tasks} /><ProgressMeter label="Processes" progress={card.processes} /></div></section>
      <section className="detail-panel" aria-labelledby="anomalies-heading"><header><span><span><small>Structural health</small><h2 id="anomalies-heading">Projection diagnostics</h2></span></span></header>{card.anomalies.length ? <AnomalyList codes={card.anomalies} /> : <div className="detail-healthy"><span aria-hidden="true">✓</span><div><strong>Structure looks healthy</strong><p>No visible artifact anomalies were reported.</p></div></div>}</section></div>
  </div>;
}

export function BoardDetailPage() { return <RepositoryGate>{(active) => <BoardDetail active={active} />}</RepositoryGate>; }
