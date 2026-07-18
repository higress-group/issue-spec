import { useState } from "react";
import { useMutation, useQuery, useQueryClient } from "@tanstack/react-query";
import { Clock3, ExternalLink, LogOut, RefreshCw, Save, ShieldCheck } from "lucide-react";
import { Link, useNavigate } from "react-router-dom";
import { ErrorNotice, Field, PageHeader, Panel, StatusBadge, TextInput } from "../app/components";
import { useInspector } from "../app/problem-inspector";
import { api } from "../lib/api/resources";
import { queryKeys, useCurrentContext } from "./session";
import { Avatar } from "../app/avatar";
import { useTranslation } from "react-i18next";
import { ProfileEmailControls } from "./profile-email-controls";

export function AccountPage() {
  const { t, i18n } = useTranslation();
  const context = useCurrentContext();
  const profile = useQuery({ queryKey: ["profile"], queryFn: ({ signal }) => api.profile(signal) });
  const client = useQueryClient();
  const navigate = useNavigate();
  const inspector = useInspector();
  const [nickname, setNickname] = useState<string | null>(null);
  const nicknameValue = nickname ?? profile.data?.nickname ?? "";
  const rotate = useMutation({ mutationFn: api.rotateSession, onSuccess: () => { inspector.note(t("account.rotated")); void client.invalidateQueries({ queryKey: queryKeys.context }); }, onError: inspector.report });
  const logout = useMutation({ mutationFn: api.logout, onSuccess: () => { client.clear(); navigate("/login", { replace: true }); }, onError: inspector.report });
  const updateProfile = useMutation({
    mutationFn: () => api.updateProfile({ nickname: nicknameValue, expected_version: profile.data?.representation_version ?? 0 }),
    onSuccess: (updated) => {
      client.setQueryData(["profile"], updated);
      setNickname(updated.nickname ?? "");
      void client.invalidateQueries({ queryKey: queryKeys.context });
      inspector.note(t("account.nicknameSaved"));
    },
    onError: inspector.report,
  });
  if (context.error) return <ErrorNotice error={context.error} />;
  const data = context.data;
  return <div className="page"><PageHeader eyebrow={t("account.eyebrow")} title={t("account.title")} description={t("account.description")} />
    <div className="two-column"><Panel title={t("account.identity")}><div className="profile-line"><Avatar login={data?.user.login ?? ""} displayName={data?.user.display_name} src={data?.user.avatar_url} size={52} /><div><strong>{data?.user.display_name}</strong><span className="mono">@{data?.user.login}</span></div><StatusBadge tone={data?.user.site_admin ? "purple" : "neutral"}>{data?.user.site_admin ? t("account.siteAdmin") : t("account.member")}</StatusBadge></div>{data?.user.login ? <Link className="text-link profile-link" to={`/users/${encodeURIComponent(data.user.login)}`}><ExternalLink size={15} />{t("account.viewProfile")}</Link> : null}</Panel>
      <Panel title={t("account.credential")}><dl className="detail-list"><div><dt>{t("account.realm")}</dt><dd><ShieldCheck size={16} />{data?.credential.kind}</dd></div><div><dt>{t("account.idleExpiry")}</dt><dd><Clock3 size={16} />{formatDate(data?.credential.idle_expires_at, i18n.resolvedLanguage, t("account.notReported"))}</dd></div><div><dt>{t("account.absoluteExpiry")}</dt><dd><Clock3 size={16} />{formatDate(data?.credential.absolute_expires_at, i18n.resolvedLanguage, t("account.notReported"))}</dd></div></dl></Panel></div>
    <Panel title={t("account.profileTitle")} description={t("account.profileHelp")}><form className="inline-form" onSubmit={(event) => { event.preventDefault(); updateProfile.mutate(undefined); }}><Field label={t("account.nickname")} hint={t("account.nicknameHint", { identityName: profile.data?.identity_display_name ?? data?.user.display_name ?? "" })}><TextInput value={nicknameValue} maxLength={80} onChange={(event) => setNickname(event.target.value)} placeholder={t("account.nicknamePlaceholder")} disabled={profile.isLoading || updateProfile.isPending} /></Field><button className="button primary" type="submit" disabled={!profile.data || updateProfile.isPending || nicknameValue.trim() === (profile.data.nickname ?? "")}><Save size={16} />{updateProfile.isPending ? t("account.savingNickname") : t("account.saveNickname")}</button></form>{profile.error ? <ErrorNotice error={profile.error} /> : null}{updateProfile.error ? <ErrorNotice error={updateProfile.error} /> : null}</Panel>
    {profile.data?.notification_email_available ? <ProfileEmailControls profile={profile.data} /> : null}
    <Panel title={t("account.controls")} description={t("account.controlsHelp")}><div className="button-row"><button className="button secondary" type="button" onClick={() => rotate.mutate(undefined)} disabled={rotate.isPending}><RefreshCw size={16} />{t("account.rotate")}</button><button className="button danger" type="button" onClick={() => logout.mutate(undefined)} disabled={logout.isPending}><LogOut size={16} />{t("account.signOut")}</button></div></Panel>
  </div>;
}

const formatDate = (value: string | undefined, locale: string | undefined, fallback: string) => value ? new Intl.DateTimeFormat(locale, { dateStyle: "medium", timeStyle: "short" }).format(new Date(value)) : fallback;
