import { ExternalLink, GitMerge, GitPullRequest, Link2Off, ShieldAlert } from "lucide-react";
import { codeChangeKind, safeCodeChangeURL, type CodeChangeRelationship } from "../../lib/api/relationships";
import { useTranslation } from "react-i18next";
import "./relationships.css";

function ProviderIcon({ relationship }: { relationship: CodeChangeRelationship }) {
  const kind = codeChangeKind(relationship);
  if (kind === "Pull request") return <GitPullRequest aria-hidden="true" />;
  if (kind === "Merge request") return <GitMerge aria-hidden="true" />;
  return <ExternalLink aria-hidden="true" />;
}

function RelationshipItem({ relationship }: { relationship: CodeChangeRelationship }) {
  const { t } = useTranslation();
  const kind = codeChangeKind(relationship);
  const kindLabel = t(kind === "Pull request" ? "changes.relationships.pullRequest" : kind === "Merge request" ? "changes.relationships.mergeRequest" : "changes.relationships.codeChange");
  const title = relationship.title?.trim() || `${kindLabel} ${relationship.external_id}`;
  const target = safeCodeChangeURL(relationship.canonical_url);
  const binding = relationship.source_binding_match === "matched"
    ? { label: t("changes.relationships.matchedLabel"), description: t("changes.relationships.matchedDescription") }
    : relationship.source_binding_match === "mismatched"
      ? { label: t("changes.relationships.mismatchedLabel"), description: t("changes.relationships.mismatchedDescription") }
      : { label: t("changes.relationships.unboundLabel"), description: t("changes.relationships.unboundDescription") };
  return <li className={`code-change-item binding-${relationship.source_binding_match}`}>
    <span className="code-change-icon"><ProviderIcon relationship={relationship} /></span>
    <span className="code-change-copy">
      <span className="code-change-kind">{kindLabel}</span>
      {target ? <a href={target} target="_blank" rel="noopener noreferrer" referrerPolicy="no-referrer">{title}<ExternalLink aria-hidden="true" /></a> : <strong>{title}</strong>}
      <small>{relationship.provider_key} · {relationship.external_repository_id} · {relationship.external_id}</small>
    </span>
    <span className="binding-state" title={binding.description}>{relationship.source_binding_match === "mismatched" ? <ShieldAlert aria-hidden="true" /> : relationship.source_binding_match === "unbound" ? <Link2Off aria-hidden="true" /> : null}{binding.label}</span>
    {!target ? <span className="unsafe-link-note"><ShieldAlert aria-hidden="true" />{t("changes.relationships.externalUnavailable")}</span> : null}
  </li>;
}

export function CodeChangeList({ relationships, empty }: { relationships: CodeChangeRelationship[]; empty?: string }) {
  const { t } = useTranslation();
  if (!relationships.length) return <p className="code-change-empty">{empty ?? t("changes.relationships.empty")}</p>;
  return <ul className="code-change-list">{relationships.map((relationship) => <RelationshipItem key={`${relationship.provider_key}:${relationship.external_repository_id}:${relationship.external_id}`} relationship={relationship} />)}</ul>;
}

export function CodeChangeIndicator({ relationships }: { relationships: CodeChangeRelationship[] }) {
  const { t } = useTranslation();
  if (!relationships.length) return null;
  const kinds = [...new Set(relationships.map((relationship) => { const kind = codeChangeKind(relationship); return t(kind === "Pull request" ? "changes.relationships.pullRequest" : kind === "Merge request" ? "changes.relationships.mergeRequest" : "changes.relationships.codeChange"); }))];
  const hasMismatch = relationships.some((relationship) => relationship.source_binding_match === "mismatched");
  return <span className={`code-change-indicator ${hasMismatch ? "has-mismatch" : ""}`} aria-label={`${t(relationships.length === 1 ? "changes.relationships.linkedOne" : "changes.relationships.linkedOther", { count: relationships.length })}${hasMismatch ? t("changes.relationships.bindingMismatchSuffix") : ""}`}>
    <GitPullRequest aria-hidden="true" /><strong>{relationships.length}</strong><span>{kinds.length === 1 ? kinds[0] : t("changes.relationships.codeChanges")}</span>{hasMismatch ? <ShieldAlert aria-hidden="true" /> : null}
  </span>;
}
