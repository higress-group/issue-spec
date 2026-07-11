import { useQuery } from "@tanstack/react-query";
import { ArrowRight, Code2, KeyRound, ShieldCheck } from "lucide-react";
import { useLocation } from "react-router-dom";
import { ErrorNotice, Loading, Panel, StatusBadge } from "../app/components";
import { api } from "../lib/api/resources";
import { queryKeys, useMeta } from "./session";

export function LoginPage() {
  const providers = useQuery({ queryKey: queryKeys.providers, queryFn: ({ signal }) => api.providers(signal) });
  const meta = useMeta();
  const location = useLocation();
  const candidate = (location.state as { returnTo?: string } | null)?.returnTo;
  const returnTo = candidate?.startsWith("/") && !candidate.startsWith("//") ? candidate : "/";
  return <div className="auth-layout">
    <section className="auth-story">
      <StatusBadge tone="teal">Self-hosted · issue-only</StatusBadge>
      <h1>Carry a change from intent to evidence.</h1>
      <p>A quiet control room for proposals, implementation threads, reviews, and the decisions that connect them.</p>
      <ol className="story-steps"><li><span>01</span>Shape the change</li><li><span>02</span>Run the workflow</li><li><span>03</span>Inspect evidence</li></ol>
    </section>
    <Panel className="auth-card">
      <span className="eyebrow">Welcome back</span><h2>Sign in to your desk</h2>
      <p>Use an identity provider configured by your server operator.</p>
      {providers.isLoading ? <Loading label="Discovering providers" /> : null}
      {providers.error ? <ErrorNotice error={providers.error} /> : null}
      <div className="provider-list">
        {providers.data?.providers.map((provider) => <a className="provider-button" href={`/api/v1/auth/${encodeURIComponent(provider.name)}/login?return_to=${encodeURIComponent(returnTo)}`} key={provider.name}>
          {provider.kind === "github" ? <Code2 size={20} /> : <ShieldCheck size={20} />}<span><strong>{provider.name}</strong><small>{provider.kind}</small></span><ArrowRight size={18} />
        </a>)}
      </div>
      {!providers.isLoading && providers.data?.providers.length === 0 ? <div className="notice"><KeyRound size={18} /><p>No interactive provider is configured. Ask an operator to enable OIDC or GitHub OAuth.</p></div> : null}
      {meta.data?.features.bootstrap ? <a className="text-link" href="/bootstrap">Setting up the first administrator?</a> : null}
    </Panel>
  </div>;
}
