import { useQuery } from "@tanstack/react-query";
import { ArrowRight, GitPullRequest, KeyRound, ShieldCheck, WifiOff } from "lucide-react";
import { useLocation } from "react-router-dom";
import { ErrorNotice, Loading, Panel, StatusBadge } from "../app/components";
import { api } from "../lib/api/resources";
import { queryKeys, useMeta } from "./session";
import { useTranslation } from "react-i18next";

export function LoginPage() {
  const { t } = useTranslation();
  const providers = useQuery({ queryKey: queryKeys.providers, queryFn: ({ signal }) => api.providers(signal) });
  const meta = useMeta();
  const location = useLocation();
  const candidate = (location.state as { returnTo?: string } | null)?.returnTo;
  const returnTo = candidate?.startsWith("/") && !candidate.startsWith("//") ? candidate : "/";
  return <div className="auth-layout">
    <section className="auth-story">
      <StatusBadge tone="teal">{t("login.badge")}</StatusBadge>
      <h1>{t("login.title")}</h1>
      <p>{t("login.description")}</p>
      <ol className="story-steps"><li><span>01</span>{t("login.steps.shape")}</li><li><span>02</span>{t("login.steps.run")}</li><li><span>03</span>{t("login.steps.inspect")}</li></ol>
    </section>
    <Panel className="auth-card">
      <span className="eyebrow">{t("login.welcome")}</span><h2>{t("login.signIn")}</h2>
      <p>{t("login.providerHelp")}</p>
      {meta.data?.transport_posture === "trusted-internal-http" ? <div className="transport-notice" role="note"><WifiOff size={18} /><span><strong>{t("login.trustedHttp")}</strong><small>{t("login.trustedHttpHelp")}</small></span></div> : null}
      {providers.isLoading ? <Loading label={t("login.discovering")} /> : null}
      {providers.error ? <ErrorNotice error={providers.error} /> : null}
      <div className="provider-list">
        {providers.data?.providers.map((provider) => <a className="provider-button" href={`/api/v1/auth/${encodeURIComponent(provider.name)}/login?return_to=${encodeURIComponent(returnTo)}`} key={provider.name}>
          {provider.kind === "github-oauth" || provider.kind === "github" ? <GitPullRequest size={20} aria-hidden="true" /> : <ShieldCheck size={20} aria-hidden="true" />}<span><strong>{provider.name}</strong><small>{provider.kind}</small></span><ArrowRight size={18} aria-hidden="true" />
        </a>)}
      </div>
      {!providers.isLoading && providers.data?.providers.length === 0 ? <div className="notice"><KeyRound size={18} /><p>{t("login.noProvider")}</p></div> : null}
      {meta.data?.features.bootstrap ? <a className="text-link" href="/bootstrap">{t("login.bootstrap")}</a> : null}
    </Panel>
  </div>;
}
