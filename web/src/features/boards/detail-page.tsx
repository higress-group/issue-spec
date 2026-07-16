import { useQuery } from "@tanstack/react-query";
import { ArrowLeft, GitBranch, GitPullRequest, Layers3, ShieldAlert } from "lucide-react";
import { Link, useParams } from "react-router-dom";
import { isApiProblem } from "../../lib/api/client";
import { boardApi } from "./api";
import { AnomalyList, BoardState, LifecycleBadge, ProgressMeter, StagePipeline } from "./components";
import { SafeBoardState } from "./board-page";
import { RepositoryGate, repositoryChangePath, type ActiveRepository } from "../issues/repository-context";
import { CodeChangeList } from "../changes/relationships";
import { useTranslation } from "react-i18next";

function BoardDetail({ active }: { active: ActiveRepository }) {
  const { t } = useTranslation();
  const { change = "" } = useParams();
  const orgId = active.organization.id;
  const repoId = active.repository.repository.id;
  const detail = useQuery({ queryKey: ["boards", orgId, repoId, "detail", change], queryFn: ({ signal }) => boardApi.change(orgId, repoId, change, signal), enabled: Boolean(orgId && repoId && change) });
  if (detail.isLoading) return <BoardState>{t("changes.detail.opening")}</BoardState>;
  if (isApiProblem(detail.error, "not_found") || isApiProblem(detail.error, "forbidden")) return <SafeBoardState />;
  if (detail.error || !detail.data) return <BoardState kind="error"><h1>{t("changes.detail.unavailableTitle")}</h1><p>{t("changes.detail.unavailableDescription")}</p></BoardState>;
  const card = detail.data;
  return <div className="board-page board-detail">
    <header className="detail-masthead"><Link className="board-back" to={repositoryChangePath(active)}><ArrowLeft aria-hidden="true" />{card.repository.display_name}</Link><div className="detail-title"><div><span className="board-eyebrow">{t("changes.detail.eyebrow")}</span><h1>{card.title}</h1><code>{card.change_key}</code></div><LifecycleBadge lifecycle={card.lifecycle} /></div><p>{t("changes.detail.description")}</p></header>
    <section className="detail-panel pipeline-panel" aria-labelledby="pipeline-heading"><header><span><Layers3 aria-hidden="true" /><span><small>{t("changes.detail.artifactChain")}</small><h2 id="pipeline-heading">{t("changes.detail.intentToImplementation")}</h2></span></span><strong>{t("changes.detail.currentStage", { stage: t(`changes.stage.${card.current_stage}`) })}</strong></header><StagePipeline card={card} owner={active.organization.name} expanded /></section>
    <section className="detail-panel code-change-panel" aria-labelledby="code-changes-heading"><header><span><GitPullRequest aria-hidden="true" /><span><small>{t("changes.detail.linkedDelivery")}</small><h2 id="code-changes-heading">{t("changes.detail.codeChanges")}</h2></span></span><strong>{t("changes.detail.activeCount", { count: card.code_changes.length })}</strong></header><CodeChangeList relationships={card.code_changes} empty={t("changes.detail.noCodeChange")} />{card.code_changes.some((relationship) => relationship.source_binding_match === "mismatched") ? <div className="binding-diagnostic" role="note"><ShieldAlert aria-hidden="true" /><span><strong>{t("changes.detail.bindingMismatch")}</strong><small>{t("changes.detail.bindingMismatchDescription")}</small></span></div> : null}</section>
    <div className="detail-columns"><section className="detail-panel" aria-labelledby="delivery-heading"><header><span><GitBranch aria-hidden="true" /><span><small>{t("changes.detail.deliveryPulse")}</small><h2 id="delivery-heading">{t("changes.detail.executionProgress")}</h2></span></span></header><div className="detail-progress"><ProgressMeter label={t("changes.card.tasks")} progress={card.tasks} /><ProgressMeter label={t("changes.card.processes")} progress={card.processes} /></div></section>
      <section className="detail-panel" aria-labelledby="anomalies-heading"><header><span><span><small>{t("changes.detail.structuralHealth")}</small><h2 id="anomalies-heading">{t("changes.detail.diagnostics")}</h2></span></span></header>{card.anomalies.length ? <AnomalyList codes={card.anomalies} /> : <div className="detail-healthy"><span aria-hidden="true">✓</span><div><strong>{t("changes.detail.healthyTitle")}</strong><p>{t("changes.detail.healthyDescription")}</p></div></div>}</section></div>
  </div>;
}

export function BoardDetailPage() { return <RepositoryGate>{(active) => <BoardDetail active={active} />}</RepositoryGate>; }
