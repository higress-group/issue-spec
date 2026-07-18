import { useState } from "react";
import { useMutation, useQueryClient } from "@tanstack/react-query";
import { Mail, RefreshCw, Trash2 } from "lucide-react";
import { ErrorNotice, Field, Panel, StatusBadge, TextInput } from "../app/components";
import { api } from "../lib/api/resources";
import type { Profile } from "../lib/api/types";
import { useTranslation } from "react-i18next";

export function ProfileEmailControls({ profile }: { profile: Profile }) {
  const { t, i18n } = useTranslation();
  const client = useQueryClient();
  const [email, setEmail] = useState(profile.pending_notification_email?.email ?? profile.notification_email ?? "");
  const refresh = () => client.invalidateQueries({ queryKey: ["profile"] });
  const bind = useMutation({
    mutationFn: () => api.setProfileEmail({ email: email.trim(), expected_version: profile.representation_version }),
    onSuccess: refresh,
  });
  const resend = useMutation({
    mutationFn: () => api.resendProfileEmail({ expected_version: profile.representation_version,
      expected_verification_version: profile.pending_notification_email?.representation_version ?? 0 }),
    onSuccess: refresh,
  });
  const remove = useMutation({ mutationFn: () => api.removeProfileEmail(profile.representation_version), onSuccess: () => { setEmail(""); return refresh(); } });
  const error = bind.error ?? resend.error ?? remove.error;
  const suffixes = profile.allowed_email_domain_suffixes;
  const emailHint = suffixes.length > 0
    ? `${t("account.notificationEmailHint")} ${t("account.notificationEmailDomainHint", { domains: suffixes.map((suffix) => `@${suffix}`).join(", ") })}`
    : t("account.notificationEmailHint");

  return <Panel title={t("account.emailTitle")} description={t("account.emailHelp")}>
    {profile.notification_email ? <div className="email-status" data-testid="verified-notification-email"><div><strong>{profile.notification_email}</strong><span>{t("account.verifiedAt", { date: formatDate(profile.notification_email_verified_at, i18n.resolvedLanguage) })}</span></div><StatusBadge tone="teal">{t("account.verified")}</StatusBadge></div> : null}
    {profile.pending_notification_email ? <div className="email-status pending" data-testid="pending-notification-email"><div><strong>{profile.pending_notification_email.email}</strong><span>{t("account.pendingUntil", { date: formatDate(profile.pending_notification_email.expires_at, i18n.resolvedLanguage) })}</span></div><StatusBadge tone="coral">{t("account.pending")}</StatusBadge></div> : null}
    <form className="inline-form" onSubmit={(event) => { event.preventDefault(); bind.mutate(); }}>
      <Field label={profile.notification_email ? t("account.replaceEmail") : t("account.notificationEmail")} hint={emailHint}>
        <TextInput type="email" value={email} maxLength={320} autoComplete="email" required onChange={(event) => setEmail(event.target.value)} disabled={bind.isPending} data-testid="notification-email-input" />
      </Field>
      <button className="button primary" type="submit" disabled={bind.isPending || !email.trim()} data-testid="notification-email-submit"><Mail size={16} />{bind.isPending ? t("account.requestingEmail") : t("account.requestEmail")}</button>
    </form>
    <div className="button-row">
      {profile.pending_notification_email ? <button className="button secondary" type="button" onClick={() => resend.mutate()} disabled={resend.isPending} data-testid="notification-email-resend"><RefreshCw size={16} />{t("account.resendEmail")}</button> : null}
      {profile.notification_email || profile.pending_notification_email ? <button className="button danger" type="button" onClick={() => remove.mutate()} disabled={remove.isPending} data-testid="notification-email-remove"><Trash2 size={16} />{t("account.removeEmail")}</button> : null}
    </div>
    {error ? <ErrorNotice error={error} /> : null}
  </Panel>;
}

function formatDate(value: string | null | undefined, locale?: string) {
  return value ? new Intl.DateTimeFormat(locale, { dateStyle: "medium", timeStyle: "short" }).format(new Date(value)) : "—";
}
