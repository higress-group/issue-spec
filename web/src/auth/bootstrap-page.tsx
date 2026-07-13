import { useState } from "react";
import { useMutation, useQuery, useQueryClient } from "@tanstack/react-query";
import { useForm } from "react-hook-form";
import { ArrowRight, KeyRound, ShieldAlert } from "lucide-react";
import { useNavigate } from "react-router-dom";
import { EmptyState, ErrorNotice, Field, Loading, Panel, TextInput } from "../app/components";
import { useInspector } from "../app/problem-inspector";
import { api } from "../lib/api/resources";
import { queryKeys } from "./session";
import { useTranslation } from "react-i18next";

type BootstrapForm = { secret: string; login: string; display_name: string; email: string };

export function BootstrapPage() {
  const { t } = useTranslation();
  const status = useQuery({ queryKey: queryKeys.bootstrap, queryFn: ({ signal }) => api.bootstrap(signal) });
  const { register, handleSubmit, reset, formState: { errors } } = useForm<BootstrapForm>({ defaultValues: { secret: "", login: "", display_name: "", email: "" } });
  const [pendingRecovery, setPendingRecovery] = useState("");
  const inspector = useInspector();
  const client = useQueryClient();
  const navigate = useNavigate();
  const finishTakeover = async (token: string) => {
    await api.exchangeRecovery(token);
    setPendingRecovery("");
    await client.invalidateQueries({ queryKey: queryKeys.context });
    navigate("/admin", { replace: true });
  };
  const claim = useMutation({
    mutationFn: async (form: BootstrapForm) => {
      const result = await api.claimBootstrap({ secret: form.secret, login: form.login, display_name: form.display_name, email: form.email || undefined });
      setPendingRecovery(result.recovery.token);
      await finishTakeover(result.recovery.token);
    },
    onSuccess: () => reset(),
    onError: (error) => inspector.report(error),
  });
  const retry = useMutation({ mutationFn: () => finishTakeover(pendingRecovery), onError: (error) => inspector.report(error) });
  if (status.isLoading) return <Loading label={t("bootstrap.checking")} />;
  if (status.error) return <ErrorNotice error={status.error} />;
  if (!status.data?.available) return <div className="public-narrow"><EmptyState title={t("bootstrap.closedTitle")} description={t("bootstrap.closedDescription")} action={<a className="button primary" href="/login">{t("bootstrap.continueSignIn")}</a>} /></div>;
  return <div className="public-narrow">
    <Panel className="bootstrap-card">
      <span className="eyebrow coral-text">{t("bootstrap.eyebrow")}</span>
      <h1>{t("bootstrap.title")}</h1>
      <p>{t("bootstrap.description")}</p>
      <div className="notice warning"><ShieldAlert size={20} /><p>{t("bootstrap.warning")}</p></div>
      <form className="form-grid" onSubmit={handleSubmit((form) => claim.mutate(form))}>
        <Field label={t("bootstrap.secret")} error={errors.secret?.message}><TextInput type="password" autoComplete="off" {...register("secret", { required: t("bootstrap.secretRequired") })} /></Field>
        <Field label={t("bootstrap.localLogin")} hint={t("bootstrap.localLoginHint")} error={errors.login?.message}><TextInput autoComplete="username" {...register("login", { required: t("bootstrap.loginRequired") })} /></Field>
        <Field label={t("bootstrap.displayName")} error={errors.display_name?.message}><TextInput autoComplete="name" {...register("display_name", { required: t("bootstrap.displayNameRequired") })} /></Field>
        <Field label={t("bootstrap.emailOptional")}><TextInput type="email" autoComplete="email" {...register("email")} /></Field>
        {claim.error ? <ErrorNotice error={claim.error} /> : null}
        <button className="button primary" type="submit" disabled={claim.isPending}><KeyRound size={17} />{claim.isPending ? t("bootstrap.claiming") : t("bootstrap.claim")}<ArrowRight size={17} /></button>
      </form>
      {pendingRecovery && claim.isError ? <div className="recovery-retry"><p>{t("bootstrap.retryDescription")}</p><button className="button secondary" type="button" onClick={() => retry.mutate()} disabled={retry.isPending}>{t("bootstrap.retry")}</button></div> : null}
    </Panel>
  </div>;
}
