import { useQuery } from "@tanstack/react-query";
import { Cable, RadioTower, Settings2, Users } from "lucide-react";
import { Link, NavLink, useParams } from "react-router-dom";
import { PageHeader } from "../app/components";
import { queryKeys } from "../auth/session";
import { api } from "../lib/api/resources";
import type { AdminRepository } from "../lib/api/types";

export type RepositorySection = "settings" | "collaborators" | "source" | "webhooks";

const sectionLabels: Record<RepositorySection, string> = {
  settings: "Settings",
  collaborators: "Collaborators",
  source: "Source",
  webhooks: "Webhooks",
};

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
  const base = `/orgs/${repository.organization_id}/repos/${repository.id}`;
  const sections: Array<{ id: RepositorySection; label: string; href: string; icon: typeof Settings2 }> = [
    { id: "settings", label: "Settings", href: `${base}/settings`, icon: Settings2 },
    { id: "collaborators", label: "Collaborators", href: `${base}/collaborators`, icon: Users },
    { id: "source", label: "Source", href: `${base}/integrations/source`, icon: Cable },
    { id: "webhooks", label: "Webhooks", href: `${base}/integrations/webhooks`, icon: RadioTower },
  ];
  return <>
    <nav className="repository-breadcrumbs" aria-label="Repository breadcrumb">
      <ol>
        <li><Link to={`/orgs/${repository.organization_id}/repos`}>Repositories</Link></li>
        <li>{section === "settings" ? <span aria-current="page">{repository.display_name}</span> : <Link to={`${base}/settings`}>{repository.display_name}</Link>}</li>
        {section !== "settings" ? <li><span aria-current="page">{sectionLabels[section]}</span></li> : null}
      </ol>
    </nav>
    <PageHeader eyebrow={`Repository / ${sectionLabels[section]}`} title={title} description={description} actions={
      <nav className="repository-section-nav" aria-label="Repository sections">
        {sections.map(({ id, label, href, icon: Icon }) => <NavLink key={id} to={href} end aria-current={section === id ? "page" : undefined} className={section === id ? "active" : undefined}><Icon size={15} aria-hidden="true" />{label}</NavLink>)}
      </nav>
    } />
  </>;
}
