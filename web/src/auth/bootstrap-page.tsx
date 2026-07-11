import { useState } from "react";
import { useMutation, useQuery, useQueryClient } from "@tanstack/react-query";
import { useForm } from "react-hook-form";
import { ArrowRight, KeyRound, ShieldAlert } from "lucide-react";
import { useNavigate } from "react-router-dom";
import { EmptyState, ErrorNotice, Field, Loading, Panel, TextInput } from "../app/components";
import { useInspector } from "../app/problem-inspector";
import { api } from "../lib/api/resources";
import { queryKeys } from "./session";

type BootstrapForm = { secret: string; login: string; display_name: string; email: string };

export function BootstrapPage() {
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
  if (status.isLoading) return <Loading label="Checking bootstrap state" />;
  if (status.error) return <ErrorNotice error={status.error} />;
  if (!status.data?.available) return <div className="public-narrow"><EmptyState title="Bootstrap is closed" description="The one-time administrator claim has already completed or this server was not started with a bootstrap secret." action={<a className="button primary" href="/login">Continue to sign in</a>} /></div>;
  return <div className="public-narrow">
    <Panel className="bootstrap-card">
      <span className="eyebrow coral-text">One-time operation</span>
      <h1>Claim the first administrator</h1>
      <p>This uses the operator-provided bootstrap secret once, then exchanges the short-lived recovery credential for a normal browser session.</p>
      <div className="notice warning"><ShieldAlert size={20} /><p>Use this page only from a trusted workstation. Secrets are never stored in browser storage.</p></div>
      <form className="form-grid" onSubmit={handleSubmit((form) => claim.mutate(form))}>
        <Field label="Bootstrap secret" error={errors.secret?.message}><TextInput type="password" autoComplete="off" {...register("secret", { required: "Bootstrap secret is required" })} /></Field>
        <Field label="Local login" hint="Immutable identifier used in issue authorship." error={errors.login?.message}><TextInput autoComplete="username" {...register("login", { required: "Login is required" })} /></Field>
        <Field label="Display name" error={errors.display_name?.message}><TextInput autoComplete="name" {...register("display_name", { required: "Display name is required" })} /></Field>
        <Field label="Email (optional)"><TextInput type="email" autoComplete="email" {...register("email")} /></Field>
        {claim.error ? <ErrorNotice error={claim.error} /> : null}
        <button className="button primary" type="submit" disabled={claim.isPending}><KeyRound size={17} />{claim.isPending ? "Claiming…" : "Claim and take over"}<ArrowRight size={17} /></button>
      </form>
      {pendingRecovery && claim.isError ? <div className="recovery-retry"><p>The claim succeeded but session takeover did not finish. Retry while this one-time credential remains in memory.</p><button className="button secondary" type="button" onClick={() => retry.mutate()} disabled={retry.isPending}>Retry takeover</button></div> : null}
    </Panel>
  </div>;
}
