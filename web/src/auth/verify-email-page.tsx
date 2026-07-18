import { useState } from "react";
import { useMutation, useQueryClient } from "@tanstack/react-query";
import { CheckCircle2, MailCheck } from "lucide-react";
import { useLocation } from "react-router-dom";
import { ErrorNotice, PageHeader, Panel } from "../app/components";
import { api } from "../lib/api/resources";
import { useTranslation } from "react-i18next";

export function VerifyEmailPage() {
  const { t } = useTranslation();
  const location = useLocation();
  const client = useQueryClient();
  const [token, setToken] = useState(() => captureVerificationToken(location.hash));
  const [confirmed, setConfirmed] = useState(false);
  const confirm = useMutation({
    mutationFn: () => api.confirmProfileEmail(token),
    onSuccess: async () => {
      setConfirmed(true);
      setToken("");
      await client.invalidateQueries({ queryKey: ["profile"] });
    },
  });

  if (!token && !confirmed) return <div className="page"><PageHeader eyebrow={t("verifyEmail.eyebrow")} title={t("verifyEmail.invalidTitle")} description={t("verifyEmail.invalidDescription")} /></div>;
  return <div className="page verify-email-page" data-testid="verify-email-page">
    <PageHeader eyebrow={t("verifyEmail.eyebrow")} title={confirmed ? t("verifyEmail.confirmedTitle") : t("verifyEmail.title")} description={confirmed ? t("verifyEmail.confirmedDescription") : t("verifyEmail.description")} />
    <Panel title={confirmed ? t("verifyEmail.done") : t("verifyEmail.review")}>
      {confirmed ? <div className="empty-state"><CheckCircle2 size={40} aria-hidden="true" /><p>{t("verifyEmail.confirmedDescription")}</p></div> : null}
      {!confirmed ? <div className="verification-review"><button className="button primary" type="button" onClick={() => confirm.mutate()} disabled={confirm.isPending} data-testid="confirm-notification-email"><MailCheck size={17} />{confirm.isPending ? t("verifyEmail.confirming") : t("verifyEmail.confirm")}</button></div> : null}
      {confirm.error ? <ErrorNotice error={confirm.error} /> : null}
    </Panel>
  </div>;
}

function captureVerificationToken(hash: string): string {
  const token = new URLSearchParams(hash.replace(/^#/, "")).get("token") ?? "";
  if (hash && typeof window !== "undefined") {
    window.history.replaceState(window.history.state, "", `${window.location.pathname}${window.location.search}`);
  }
  return token;
}
