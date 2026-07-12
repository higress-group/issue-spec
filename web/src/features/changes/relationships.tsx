import { ExternalLink, GitMerge, GitPullRequest, Link2Off, ShieldAlert } from "lucide-react";
import { codeChangeKind, safeCodeChangeURL, type CodeChangeRelationship } from "../../lib/api/relationships";
import "./relationships.css";

const bindingCopy = {
  matched: { label: "Source matched", description: "This code change belongs to the repository's active source binding." },
  mismatched: { label: "Binding mismatch", description: "This code change points at a different provider repository than the active source binding." },
  unbound: { label: "No active binding", description: "The repository has no active source binding to compare." },
} as const;

function ProviderIcon({ provider }: { provider: string }) {
  const kind = codeChangeKind(provider);
  if (kind === "Pull request") return <GitPullRequest aria-hidden="true" />;
  if (kind === "Merge request") return <GitMerge aria-hidden="true" />;
  return <ExternalLink aria-hidden="true" />;
}

function RelationshipItem({ relationship }: { relationship: CodeChangeRelationship }) {
  const kind = codeChangeKind(relationship.provider_key);
  const title = relationship.title?.trim() || `${kind} ${relationship.external_id}`;
  const target = safeCodeChangeURL(relationship.canonical_url);
  const binding = bindingCopy[relationship.source_binding_match];
  return <li className={`code-change-item binding-${relationship.source_binding_match}`}>
    <span className="code-change-icon"><ProviderIcon provider={relationship.provider_key} /></span>
    <span className="code-change-copy">
      <span className="code-change-kind">{kind}</span>
      {target ? <a href={target} target="_blank" rel="noopener noreferrer" referrerPolicy="no-referrer">{title}<ExternalLink aria-hidden="true" /></a> : <strong>{title}</strong>}
      <small>{relationship.provider_key} · {relationship.external_repository_id} · {relationship.external_id}</small>
    </span>
    <span className="binding-state" title={binding.description}>{relationship.source_binding_match === "mismatched" ? <ShieldAlert aria-hidden="true" /> : relationship.source_binding_match === "unbound" ? <Link2Off aria-hidden="true" /> : null}{binding.label}</span>
    {!target ? <span className="unsafe-link-note"><ShieldAlert aria-hidden="true" />External link unavailable</span> : null}
  </li>;
}

export function CodeChangeList({ relationships, empty = "No active code change is linked." }: { relationships: CodeChangeRelationship[]; empty?: string }) {
  if (!relationships.length) return <p className="code-change-empty">{empty}</p>;
  return <ul className="code-change-list">{relationships.map((relationship) => <RelationshipItem key={`${relationship.provider_key}:${relationship.external_repository_id}:${relationship.external_id}`} relationship={relationship} />)}</ul>;
}

export function CodeChangeIndicator({ relationships }: { relationships: CodeChangeRelationship[] }) {
  if (!relationships.length) return null;
  const kinds = [...new Set(relationships.map((relationship) => codeChangeKind(relationship.provider_key)))];
  const hasMismatch = relationships.some((relationship) => relationship.source_binding_match === "mismatched");
  return <span className={`code-change-indicator ${hasMismatch ? "has-mismatch" : ""}`} aria-label={`${relationships.length} linked code ${relationships.length === 1 ? "change" : "changes"}${hasMismatch ? ", source binding mismatch" : ""}`}>
    <GitPullRequest aria-hidden="true" /><strong>{relationships.length}</strong><span>{kinds.length === 1 ? kinds[0] : "Code changes"}</span>{hasMismatch ? <ShieldAlert aria-hidden="true" /> : null}
  </span>;
}
