import { useQuery } from "@tanstack/react-query";
import { Cable, RadioTower, Settings2, Users } from "lucide-react";
import { Link, NavLink, useParams } from "react-router-dom";
import { PageHeader } from "../app/components";
import { queryKeys, useMeta } from "../auth/session";
import { api } from "../lib/api/resources";
import type { AdminRepository } from "../lib/api/types";
import { useTranslation } from "react-i18next";
import { RepositorySubscriptionControl } from "./repository-subscription-control";
import "./repository-notifications.css";

export type RepositorySection = "settings" | "collaborators" | "source" | "webhooks";

export function useRepositoryContext() {
  const { orgId = "", repoId = "" } = useParams();
  const repository = useQuery({
    queryKey: queryKeys.repository(orgId, repoId),
    queryFn: ({ signal }) => api.repository(orgId, repoId, signal),
  });
  return { orgId, repoId, repository };
}

export function RepositoryHeader({ repository, section, title, description }: {
  repository: AdminRepository;
  section: RepositorySection;
  title: string;
  description: string;
}) {
  const { t } = useTranslation();
  const meta = useMeta();
  const sectionLabels: Record<RepositorySection, string> = {
    settings: t("common.settings"), collaborators: t("common.collaborators"), source: t("common.source"), webhooks: t("common.webhooks"),
  };
  const base = `/orgs/${repository.organization_id}/repos/${repository.id}`;
  const sections: Array<{ id: RepositorySection; label: string; href: string; icon: typeof Settings2 }> = [
    { id: "settings", label: sectionLabels.settings, href: `${base}/settings`, icon: Settings2 },
    { id: "collaborators", label: sectionLabels.collaborators, href: `${base}/collaborators`, icon: Users },
    { id: "source", label: sectionLabels.source, href: `${base}/integrations/source`, icon: Cable },
    { id: "webhooks", label: sectionLabels.webhooks, href: `${base}/integrations/webhooks`, icon: RadioTower },
  ];
  return <>
    <nav className="repository-breadcrumbs" aria-label={t("repositoryHeader.breadcrumb")}>
      <ol>
        <li><Link to={`/orgs/${repository.organization_id}/repos`}>{t("common.repositories")}</Link></li>
        <li>{section === "settings" ? <span aria-current="page">{repository.display_name}</span> : <Link to={`${base}/settings`}>{repository.display_name}</Link>}</li>
        {section !== "settings" ? <li><span aria-current="page">{sectionLabels[section]}</span></li> : null}
      </ol>
    </nav>
    <PageHeader eyebrow={t("repositoryHeader.eyebrow", { section: sectionLabels[section] })} title={title} description={description} actions={
      <div className="repository-header-actions">
        {meta.data?.features.repository_email_subscriptions ?
          <RepositorySubscriptionControl orgId={repository.organization_id} repoId={repository.id} /> : null}
        <nav className="repository-section-nav" aria-label={t("repositoryHeader.sections")}>
          {sections.map(({ id, label, href, icon: Icon }) => <NavLink key={id} to={href} end aria-current={section === id ? "page" : undefined} className={section === id ? "active" : undefined}><Icon size={15} aria-hidden="true" />{label}</NavLink>)}
        </nav>
      </div>
    } />
  </>;
}
