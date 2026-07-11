import { useMutation, useQueryClient } from "@tanstack/react-query";
import { Clock3, LogOut, RefreshCw, ShieldCheck } from "lucide-react";
import { useNavigate } from "react-router-dom";
import { ErrorNotice, PageHeader, Panel, StatusBadge } from "../app/components";
import { useInspector } from "../app/problem-inspector";
import { api } from "../lib/api/resources";
import { queryKeys, useCurrentContext } from "./session";

export function AccountPage() {
  const context = useCurrentContext();
  const client = useQueryClient();
  const navigate = useNavigate();
  const inspector = useInspector();
  const rotate = useMutation({ mutationFn: api.rotateSession, onSuccess: () => { inspector.note("Browser session rotated. The absolute expiry is unchanged."); void client.invalidateQueries({ queryKey: queryKeys.context }); }, onError: inspector.report });
  const logout = useMutation({ mutationFn: api.logout, onSuccess: () => { client.clear(); navigate("/login", { replace: true }); }, onError: inspector.report });
  if (context.error) return <ErrorNotice error={context.error} />;
  const data = context.data;
  return <div className="page"><PageHeader eyebrow="Account / session" title="A bounded browser session" description="Your identity authority and credential lifetime are separate, visible facts." />
    <div className="two-column"><Panel title="Identity"><div className="profile-line"><span className="avatar">{data?.user.login.slice(0, 2).toUpperCase()}</span><div><strong>{data?.user.display_name}</strong><span className="mono">@{data?.user.login}</span></div><StatusBadge tone={data?.user.site_admin ? "purple" : "neutral"}>{data?.user.site_admin ? "site admin" : "member"}</StatusBadge></div></Panel>
      <Panel title="Credential"><dl className="detail-list"><div><dt>Realm</dt><dd><ShieldCheck size={16} />{data?.credential.kind}</dd></div><div><dt>Idle expiry</dt><dd><Clock3 size={16} />{formatDate(data?.credential.idle_expires_at)}</dd></div><div><dt>Absolute expiry</dt><dd><Clock3 size={16} />{formatDate(data?.credential.absolute_expires_at)}</dd></div></dl></Panel></div>
    <Panel title="Session controls" description="Rotation replaces the opaque cookie and CSRF token without extending absolute lifetime."><div className="button-row"><button className="button secondary" type="button" onClick={() => rotate.mutate(undefined)} disabled={rotate.isPending}><RefreshCw size={16} />Rotate session</button><button className="button danger" type="button" onClick={() => logout.mutate(undefined)} disabled={logout.isPending}><LogOut size={16} />Sign out</button></div></Panel>
  </div>;
}

const formatDate = (value?: string) => value ? new Intl.DateTimeFormat(undefined, { dateStyle: "medium", timeStyle: "short" }).format(new Date(value)) : "Not reported";
