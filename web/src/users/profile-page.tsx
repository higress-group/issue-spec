import { useQuery } from "@tanstack/react-query";
import { Link, Navigate, useParams } from "react-router-dom";
import { AtSign, Settings2 } from "lucide-react";
import { Avatar } from "../app/avatar";
import { ErrorNotice, Loading, PageHeader, Panel } from "../app/components";
import { useCurrentContext } from "../auth/session";
import { api } from "../lib/api/resources";
import { useTranslation } from "react-i18next";

export function ProfilePage() {
  const { t } = useTranslation();
  const { login = "" } = useParams();
  const current = useCurrentContext();
  const profile = useQuery({ queryKey: ["users", login], queryFn: ({ signal }) => api.publicProfile(login, signal), enabled: Boolean(login) });
  if (profile.isLoading) return <Loading label={t("profile.loading")} />;
  if (profile.error) return <div className="page"><ErrorNotice error={profile.error} /></div>;
  if (!profile.data) return null;
  const mine = current.data?.user.login.toLowerCase() === profile.data.login.toLowerCase();
  return <div className="page profile-page"><PageHeader eyebrow={t("profile.eyebrow")} title={profile.data.display_name} description={`@${profile.data.login}`} actions={mine ? <Link className="button secondary" to="/settings/account"><Settings2 size={16} />{t("profile.edit")}</Link> : undefined} />
    <Panel><div className="public-profile-card"><Avatar login={profile.data.login} displayName={profile.data.display_name} src={profile.data.avatar_url} size={88} /><div><h2>{profile.data.display_name}</h2><p><AtSign size={16} />{profile.data.login}</p><span>{t("profile.preferredName")}</span></div></div></Panel>
  </div>;
}

export function LegacyUserIssuesRedirect() {
  const { login = "" } = useParams();
  return <Navigate to={`/users/${encodeURIComponent(login)}`} replace />;
}
