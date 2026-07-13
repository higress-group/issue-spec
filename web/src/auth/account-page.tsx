import { useMutation, useQueryClient } from "@tanstack/react-query";
import { Clock3, LogOut, RefreshCw, ShieldCheck } from "lucide-react";
import { useNavigate } from "react-router-dom";
import { ErrorNotice, PageHeader, Panel, StatusBadge } from "../app/components";
import { useInspector } from "../app/problem-inspector";
import { api } from "../lib/api/resources";
import { queryKeys, useCurrentContext } from "./session";
import { Avatar } from "../app/avatar";
import { useTranslation } from "react-i18next";

export function AccountPage() {
  const { t, i18n } = useTranslation();
  const context = useCurrentContext();
  const client = useQueryClient();
  const navigate = useNavigate();
  const inspector = useInspector();
  const rotate = useMutation({ mutationFn: api.rotateSession, onSuccess: () => { inspector.note(t("account.rotated")); void client.invalidateQueries({ queryKey: queryKeys.context }); }, onError: inspector.report });
  const logout = useMutation({ mutationFn: api.logout, onSuccess: () => { client.clear(); navigate("/login", { replace: true }); }, onError: inspector.report });
  if (context.error) return <ErrorNotice error={context.error} />;
  const data = context.data;
  return <div className="page"><PageHeader eyebrow={t("account.eyebrow")} title={t("account.title")} description={t("account.description")} />
    <div className="two-column"><Panel title={t("account.identity")}><div className="profile-line"><Avatar login={data?.user.login ?? ""} displayName={data?.user.display_name} src={data?.user.avatar_url} size={52} /><div><strong>{data?.user.display_name}</strong><span className="mono">@{data?.user.login}</span></div><StatusBadge tone={data?.user.site_admin ? "purple" : "neutral"}>{data?.user.site_admin ? t("account.siteAdmin") : t("account.member")}</StatusBadge></div></Panel>
      <Panel title={t("account.credential")}><dl className="detail-list"><div><dt>{t("account.realm")}</dt><dd><ShieldCheck size={16} />{data?.credential.kind}</dd></div><div><dt>{t("account.idleExpiry")}</dt><dd><Clock3 size={16} />{formatDate(data?.credential.idle_expires_at, i18n.resolvedLanguage, t("account.notReported"))}</dd></div><div><dt>{t("account.absoluteExpiry")}</dt><dd><Clock3 size={16} />{formatDate(data?.credential.absolute_expires_at, i18n.resolvedLanguage, t("account.notReported"))}</dd></div></dl></Panel></div>
    <Panel title={t("account.controls")} description={t("account.controlsHelp")}><div className="button-row"><button className="button secondary" type="button" onClick={() => rotate.mutate(undefined)} disabled={rotate.isPending}><RefreshCw size={16} />{t("account.rotate")}</button><button className="button danger" type="button" onClick={() => logout.mutate(undefined)} disabled={logout.isPending}><LogOut size={16} />{t("account.signOut")}</button></div></Panel>
  </div>;
}

const formatDate = (value: string | undefined, locale: string | undefined, fallback: string) => value ? new Intl.DateTimeFormat(locale, { dateStyle: "medium", timeStyle: "short" }).format(new Date(value)) : fallback;
