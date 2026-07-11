import { useQuery } from "@tanstack/react-query";
import { ArrowLeft, GitBranch, Layers3 } from "lucide-react";
import { Link, useParams } from "react-router-dom";
import { isApiProblem } from "../../lib/api/client";
import { boardApi } from "./api";
import { AnomalyList, BoardState, LifecycleBadge, ProgressMeter, StagePipeline } from "./components";
import { SafeBoardState } from "./board-page";

export function BoardDetailPage() {
  const { orgId = "", repoId = "", change = "" } = useParams();
  const detail = useQuery({ queryKey: ["boards", orgId, repoId, "detail", change], queryFn: ({ signal }) => boardApi.change(orgId, repoId, change, signal), enabled: Boolean(orgId && repoId && change) });
  if (detail.isLoading) return <BoardState>Opening change projection…</BoardState>;
  if (isApiProblem(detail.error, "not_found") || isApiProblem(detail.error, "forbidden")) return <SafeBoardState />;
  if (detail.error || !detail.data) return <BoardState kind="error"><h1>Change unavailable</h1><p>The projection could not be loaded. Try again or inspect the request ID.</p></BoardState>;
  const card = detail.data;
  return <div className="board-page board-detail">
    <header className="detail-masthead"><Link className="board-back" to={`/changes/${orgId}/repos/${repoId}`}><ArrowLeft aria-hidden="true" />{card.repository.display_name}</Link><div className="detail-title"><div><span className="board-eyebrow">Change detail</span><h1>{card.title}</h1><code>{card.change_key}</code></div><LifecycleBadge lifecycle={card.lifecycle} /></div><p>One permission-filtered view across the artifact chain and its execution records.</p></header>
    <section className="detail-panel pipeline-panel" aria-labelledby="pipeline-heading"><header><span><Layers3 aria-hidden="true" /><span><small>Artifact chain</small><h2 id="pipeline-heading">From intent to implementation</h2></span></span><strong>{card.current_stage} stage</strong></header><StagePipeline card={card} expanded /></section>
    <div className="detail-columns"><section className="detail-panel" aria-labelledby="delivery-heading"><header><span><GitBranch aria-hidden="true" /><span><small>Delivery pulse</small><h2 id="delivery-heading">Execution progress</h2></span></span></header><div className="detail-progress"><ProgressMeter label="Tasks" progress={card.tasks} /><ProgressMeter label="Processes" progress={card.processes} /></div></section>
      <section className="detail-panel" aria-labelledby="anomalies-heading"><header><span><span><small>Structural health</small><h2 id="anomalies-heading">Projection diagnostics</h2></span></span></header>{card.anomalies.length ? <AnomalyList codes={card.anomalies} /> : <div className="detail-healthy"><span aria-hidden="true">✓</span><div><strong>Structure looks healthy</strong><p>No visible artifact anomalies were reported.</p></div></div>}</section></div>
  </div>;
}
