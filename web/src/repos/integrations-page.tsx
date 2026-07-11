import { Cable, CircleOff, ExternalLink, RadioTower } from "lucide-react";
import { Link, useParams } from "react-router-dom";
import { EmptyState, PageHeader, Panel, StatusBadge } from "../app/components";
import { useMeta } from "../auth/session";

type IntegrationKind = "source" | "webhooks";

type IntegrationAdapter = {
  kind: IntegrationKind;
  capability: "source_bindings" | "webhooks";
  label: string;
  available: boolean;
  reason: string;
};

export function IntegrationsPage({ kind }: { kind: IntegrationKind }) {
  const { orgId = "", repoId = "" } = useParams();
  const meta = useMeta();
  const capability = kind === "source" ? "source_bindings" : "webhooks";
  const advertised = meta.data?.features[capability] ?? false;
  const adapter: IntegrationAdapter = {
    kind,
    capability,
    label: kind === "source" ? "Source binding" : "Webhook delivery",
    available: false,
    reason: advertised
      ? "The server advertises this capability, but its typed settings adapter is intentionally left for the owning backend process."
      : "This server did not mount the required native capability.",
  };
  return <div className="page"><PageHeader eyebrow="Repository / integrations" title={adapter.label} description="Integrations are neutral, credential-free settings surfaces—not source hosting." actions={<div className="button-row"><Link className="button secondary" to={`/orgs/${orgId}/repos/${repoId}/integrations/source`}><Cable size={16} />Source</Link><Link className="button secondary" to={`/orgs/${orgId}/repos/${repoId}/integrations/webhooks`}><RadioTower size={16} />Webhooks</Link></div>} />
    <Panel><EmptyState title={advertised ? "Adapter handoff pending" : "Capability unavailable"} description={adapter.reason} action={<StatusBadge tone={advertised ? "purple" : "coral"}>{advertised ? "advertised / not wired" : "not mounted"}</StatusBadge>} /></Panel>
    <Panel title="Boundary"><div className="editorial-note"><CircleOff size={22} /><p>No endpoint is guessed here. PROCESS-007 and PROCESS-008 own the concrete webhook and source-binding contracts; P018 advertises them only after mounting their RouteSets.</p></div><a className="text-link" href="https://github.com/higress-group/issue-spec/issues/162" target="_blank" rel="noreferrer">View implementation graph <ExternalLink size={14} /></a></Panel>
  </div>;
}
