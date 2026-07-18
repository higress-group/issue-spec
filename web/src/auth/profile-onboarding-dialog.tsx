import { useEffect, useRef, useState, type KeyboardEvent } from "react";
import { useMutation, useQuery, useQueryClient } from "@tanstack/react-query";
import { MailCheck } from "lucide-react";
import { ErrorNotice, Field, TextInput } from "../app/components";
import { api } from "../lib/api/resources";
import { useTranslation } from "react-i18next";

export function ProfileOnboardingDialog({ enabled }: { enabled: boolean }) {
  const { t } = useTranslation();
  const client = useQueryClient();
  const profile = useQuery({ queryKey: ["profile"], queryFn: ({ signal }) => api.profile(signal), enabled });
  const [name, setName] = useState<string | null>(null);
  const [email, setEmail] = useState("");
  const dialog = useRef<HTMLDivElement>(null);
  const nameValue = name ?? profile.data?.nickname ?? profile.data?.display_name ?? "";
  const visible = enabled && Boolean(profile.data && !profile.data.onboarding_completed);
  const suffixes = profile.data?.allowed_email_domain_suffixes ?? [];
  const emailHint = suffixes.length > 0
    ? `${t("onboarding.emailHint")} ${t("onboarding.emailDomainHint", { domains: suffixes.map((suffix) => `@${suffix}`).join(", ") })}`
    : t("onboarding.emailHint");
  const complete = useMutation({
    mutationFn: async () => {
      if (!profile.data) throw new Error(t("onboarding.profileUnavailable"));
      return api.completeProfileOnboarding({ name: nameValue.trim(), email: email.trim(), expected_version: profile.data.representation_version });
    },
    onSuccess: async () => {
      await client.invalidateQueries({ queryKey: ["profile"] });
    },
  });

  useEffect(() => {
    if (visible) dialog.current?.querySelector<HTMLInputElement>("input")?.focus();
  }, [visible]);

  if (!enabled || (!profile.isLoading && !visible && !profile.error)) return null;
  if (profile.isLoading) return null;

  const trapFocus = (event: KeyboardEvent<HTMLDivElement>) => {
    if (event.key === "Escape") {
      event.preventDefault();
      return;
    }
    if (event.key !== "Tab" || !dialog.current) return;
    const focusable = [...dialog.current.querySelectorAll<HTMLElement>("input,button:not([disabled])")];
    if (focusable.length === 0) return;
    const first = focusable[0];
    const last = focusable[focusable.length - 1];
    if (event.shiftKey && document.activeElement === first) {
      event.preventDefault();
      last.focus();
    } else if (!event.shiftKey && document.activeElement === last) {
      event.preventDefault();
      first.focus();
    }
  };

  return <div className="modal-backdrop onboarding-backdrop">
    <div ref={dialog} className="modal-card onboarding-dialog" role="dialog" aria-modal="true" aria-labelledby="profile-onboarding-title" aria-describedby="profile-onboarding-description" data-testid="profile-onboarding-dialog" onKeyDown={trapFocus}>
      <span className="eyebrow">{t("onboarding.eyebrow")}</span>
      <h1 id="profile-onboarding-title">{t("onboarding.title")}</h1>
      <p id="profile-onboarding-description">{t("onboarding.description")}</p>
      <form onSubmit={(event) => { event.preventDefault(); complete.mutate(); }}>
        <Field label={t("onboarding.name")} hint={t("onboarding.nameHint")}>
          <TextInput value={nameValue} maxLength={80} required autoComplete="name" onChange={(event) => setName(event.target.value)} disabled={complete.isPending} data-testid="onboarding-name" />
        </Field>
        <Field label={t("onboarding.email")} hint={emailHint}>
          <TextInput type="email" value={email} maxLength={320} required autoComplete="email" onChange={(event) => setEmail(event.target.value)} disabled={complete.isPending} data-testid="onboarding-email" />
        </Field>
        <button className="button primary" type="submit" disabled={complete.isPending || !nameValue.trim() || !email.trim()} data-testid="onboarding-submit"><MailCheck size={17} />{complete.isPending ? t("onboarding.saving") : t("onboarding.continue")}</button>
      </form>
      {profile.error ? <ErrorNotice error={profile.error} /> : null}
      {complete.error ? <ErrorNotice error={complete.error} /> : null}
    </div>
  </div>;
}
