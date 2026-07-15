import { useQuery } from "@tanstack/react-query";
import { ArrowRight, LockKeyhole, PanelsTopLeft } from "lucide-react";
import { Link } from "react-router-dom";
import { useTranslation } from "react-i18next";
import { useCurrentContext } from "../../auth/session";
import { api } from "../../lib/api/resources";
import { IssueLoading, repositoryIssuePathForNames } from "./repository-context";

function OrganizationRepositories({ id, name }: { id: string; name: string }) {
  const { t } = useTranslation();
  const repositories = useQuery({ queryKey: ["context", "repositories", id], queryFn: ({ signal }) => api.repositoriesContext(id, signal) });
  if (repositories.isLoading) return <IssueLoading label={t("issues.workspace.openingRepository", { name })} />;
  if (repositories.error) return <p className="issue-inline-error">{t("issues.workspace.repositoryUnavailable", { name })}</p>;
  if (!repositories.data?.repositories.length) return <p className="issue-empty-copy">{t("issues.workspace.noRepositories")}</p>;
  return <div className="repository-cards">{repositories.data.repositories.map((item) => <Link className="repository-card" key={item.repository.id} to={repositoryIssuePathForNames(name, item.repository.name)}><span className="repository-icon"><PanelsTopLeft aria-hidden="true" /></span><span><strong>{item.repository.display_name}</strong><small>{name} / {item.repository.name}</small></span><span className="permission-note"><LockKeyhole aria-hidden="true" size={14} />{t(`common.permission.${item.effective_permission}`)}</span><ArrowRight aria-hidden="true" /></Link>)}</div>;
}
export function IssueWorkspacePage() {
  const { t } = useTranslation();
  const context = useCurrentContext();
  if (context.isLoading) return <IssueLoading label={t("issues.workspace.opening")} />;
  return <div className="issue-page"><header className="issue-hero"><div><span className="issue-kicker">{t("issues.workspace.eyebrow")}</span><h1>{t("issues.workspace.title")}</h1><p>{t("issues.workspace.description")}</p></div><div className="hero-seal" aria-hidden="true"><span>IS</span><small>{t("issues.list.title")}</small></div></header>
    {context.data?.organizations.length ? context.data.organizations.map((org) => <section className="repository-section" key={org.id}><header><div><span className="issue-kicker purple">{t("issues.workspace.organization")}</span><h2>{org.display_name}</h2></div><span className="permission-pill">{t(`common.permission.${org.effective_permission}`)}</span></header><OrganizationRepositories id={org.id} name={org.name} /></section>) : <div className="issue-status"><h2>{t("issues.workspace.emptyTitle")}</h2><p>{t("issues.workspace.emptyDescription")}</p></div>}
  </div>;
}
