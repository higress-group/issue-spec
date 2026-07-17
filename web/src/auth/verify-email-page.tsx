import { useState } from "react";
import { useMutation, useQuery, useQueryClient } from "@tanstack/react-query";
import { CheckCircle2, MailCheck } from "lucide-react";
import { useLocation } from "react-router-dom";
import { ErrorNotice, Loading, PageHeader, Panel } from "../app/components";
import { api } from "../lib/api/resources";
import { useTranslation } from "react-i18next";

export function VerifyEmailPage() {
  const { t } = useTranslation();
  const location = useLocation();
  const client = useQueryClient();
  const token = new URLSearchParams(location.hash.replace(/^#/, "")).get("token") ?? "";
  const [confirmed, setConfirmed] = useState(false);
  const inspection = useQuery({
    queryKey: ["profile-email-verification", token],
    queryFn: ({ signal }) => api.inspectProfileEmail(token, signal),
    enabled: Boolean(token),
    retry: false,
  });
  const confirm = useMutation({
    mutationFn: () => api.confirmProfileEmail(token),
    onSuccess: async () => {
      setConfirmed(true);
      await client.invalidateQueries({ queryKey: ["profile"] });
    },
  });

  if (!token) return <div className="page"><PageHeader eyebrow={t("verifyEmail.eyebrow")} title={t("verifyEmail.invalidTitle")} description={t("verifyEmail.invalidDescription")} /></div>;
  if (inspection.isLoading) return <Loading />;
  return <div className="page verify-email-page" data-testid="verify-email-page">
    <PageHeader eyebrow={t("verifyEmail.eyebrow")} title={confirmed ? t("verifyEmail.confirmedTitle") : t("verifyEmail.title")} description={confirmed ? t("verifyEmail.confirmedDescription") : t("verifyEmail.description")} />
    <Panel title={confirmed ? t("verifyEmail.done") : t("verifyEmail.review")}>
      {confirmed ? <div className="empty-state"><CheckCircle2 size={40} aria-hidden="true" /><p>{t("verifyEmail.confirmedDescription")}</p></div> : null}
      {!confirmed && inspection.data ? <div className="verification-review"><p>{t("verifyEmail.expiry", { date: new Date(inspection.data.expires_at).toLocaleString() })}</p><button className="button primary" type="button" onClick={() => confirm.mutate()} disabled={confirm.isPending} data-testid="confirm-notification-email"><MailCheck size={17} />{confirm.isPending ? t("verifyEmail.confirming") : t("verifyEmail.confirm")}</button></div> : null}
      {inspection.error ? <ErrorNotice error={inspection.error} /> : null}
      {confirm.error ? <ErrorNotice error={confirm.error} /> : null}
    </Panel>
  </div>;
}
