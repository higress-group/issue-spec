import { useQuery } from "@tanstack/react-query";
import { ArrowRight, LockKeyhole, PanelsTopLeft } from "lucide-react";
import { Link } from "react-router-dom";
import { useCurrentContext } from "../../auth/session";
import { api } from "../../lib/api/resources";
import { IssueLoading } from "./repository-context";

function OrganizationRepositories({ id, name }: { id: string; name: string }) {
  const repositories = useQuery({ queryKey: ["context", "repositories", id], queryFn: ({ signal }) => api.repositoriesContext(id, signal) });
  if (repositories.isLoading) return <IssueLoading label={`Opening ${name}`} />;
  if (repositories.error) return <p className="issue-inline-error">Repository context is unavailable for {name}.</p>;
  if (!repositories.data?.repositories.length) return <p className="issue-empty-copy">No issue-enabled repositories are visible here.</p>;
  return <div className="repository-cards">{repositories.data.repositories.map((item) => <Link className="repository-card" key={item.repository.id} to={`/issues/${id}/${item.repository.id}`}><span className="repository-icon"><PanelsTopLeft aria-hidden="true" /></span><span><strong>{item.repository.display_name}</strong><small>{name} / {item.repository.name}</small></span><span className="permission-note"><LockKeyhole aria-hidden="true" size={14} />{item.effective_permission}</span><ArrowRight aria-hidden="true" /></Link>)}</div>;
}
export function IssueWorkspacePage() {
  const context = useCurrentContext();
  if (context.isLoading) return <IssueLoading label="Opening issue desks" />;
  return <div className="issue-page"><header className="issue-hero"><div><span className="issue-kicker">Issue desk</span><h1>Work is clearer when the conversation stays close.</h1><p>Choose a repository to triage issues, preserve raw workflow artifacts, and keep decisions in one readable timeline.</p></div><div className="hero-seal" aria-hidden="true"><span>IS</span><small>ISSUES</small></div></header>
    {context.data?.organizations.length ? context.data.organizations.map((org) => <section className="repository-section" key={org.id}><header><div><span className="issue-kicker purple">Organization</span><h2>{org.display_name}</h2></div><span className="permission-pill">{org.effective_permission}</span></header><OrganizationRepositories id={org.id} name={org.name} /></section>) : <div className="issue-status"><h2>No repository desks yet</h2><p>Ask an administrator to add you to an organization.</p></div>}
  </div>;
}
