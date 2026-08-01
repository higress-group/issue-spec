import { useMutation, useQuery, useQueryClient } from "@tanstack/react-query";
import { useState } from "react";
import { useTranslation } from "react-i18next";
import { CircleHelp, ExternalLink } from "lucide-react";
import { issueApi } from "./api";
import { PreciseRelativeTime } from "./relative-time";

const questionMarkerPattern = /<!--\s*issue-spec:type=QUESTION\s+id=(QUESTION-[0-9]{3,})(?:\s[^>]*)?-->/;

// questionCommentId extracts the typed QUESTION identity from a comment body.
// The marker is a display hint only; the panel re-reads QUESTION authority
// from the server before rendering any control.
export function questionCommentId(body: string): string | null {
  const match = questionMarkerPattern.exec(body);
  return match ? match[1] : null;
}

// QuestionAnswerPanel renders and submits native answer controls under a typed
// QUESTION comment. The server revalidates the current QUESTION and appends the
// immutable ANSWER; generic HTML previews do not participate in this flow.
export function QuestionAnswerPanel({ owner, repo, number, questionId, now, canAnswer }: {
  owner: string;
  repo: string;
  number: number;
  questionId: string;
  now: number;
  canAnswer: boolean;
}) {
  const { t } = useTranslation();
  const queryClient = useQueryClient();
  const [optionIds, setOptionIds] = useState<string[]>([]);
  const [custom, setCustom] = useState("");
  const questionQueryKey = ["issues", owner, repo, number, "question", questionId] as const;
  const authority = useQuery({
    queryKey: questionQueryKey,
    queryFn: ({ signal }) => issueApi.getQuestion(owner, repo, number, questionId, signal),
    staleTime: 15_000,
    retry: false,
  });
  const mutation = useMutation({
    mutationFn: async (selection: { optionIds: string[]; custom: string }) => {
      const latest = await authority.refetch({ throwOnError: true });
      if (!latest.data) throw new Error("QUESTION authority is unavailable");
      const model = latest.data.question.choice_model;
      const trimmed = selection.custom.trim();
      if (trimmed) {
        if (!model.allow_custom || selection.optionIds.length) throw new Error("Invalid custom answer");
      } else {
        const available = new Set(model.options.map((option) => option.id));
        if (!selection.optionIds.length || selection.optionIds.some((id) => !available.has(id)) ||
            (model.mode === "single" && selection.optionIds.length !== 1)) throw new Error("Invalid option selection");
      }
      return issueApi.createAnswer(owner, repo, number, {
        question_id: questionId,
        question_digest: latest.data.body_digest,
        option_ids: trimmed ? [] : selection.optionIds,
        custom: trimmed,
      });
    },
    onSuccess: async () => {
      setOptionIds([]);
      setCustom("");
      await Promise.all([
        queryClient.invalidateQueries({ queryKey: ["issues", owner, repo, number, "comments"] }),
        queryClient.invalidateQueries({ queryKey: questionQueryKey }),
      ]);
    },
  });
  if (authority.isLoading) return <p className="question-panel-status" role="status">{t("issues.answer.panelLoading")}</p>;
  // Legacy QUESTIONs without a choice model (or transient failures) keep the
  // plain Markdown comment as the only surface.
  if (authority.error || !authority.data) return null;
  const { question, effective_answer: effective } = authority.data;
  const model = question.choice_model;
  const hasSelection = custom.trim() !== "" || optionIds.length > 0;
  const chooseOption = (id: string, checked: boolean) => {
    setCustom("");
    if (model.mode === "single") {
      setOptionIds(checked ? [id] : []);
      return;
    }
    setOptionIds((current) => checked ? [...current.filter((value) => value !== id), id] : current.filter((value) => value !== id));
  };
  const editCustom = (value: string) => {
    setCustom(value);
    if (value.trim()) setOptionIds([]);
  };
  const submit = () => {
    const trimmed = custom.trim();
    if (!canAnswer || (!trimmed && optionIds.length === 0)) return;
    mutation.mutate({ optionIds: trimmed ? [] : optionIds, custom: trimmed });
  };
  return <section className="question-answer-panel" aria-label={t("issues.answer.panelLabel", { id: questionId })}>
    <header>
      <span className="issue-kicker purple"><CircleHelp aria-hidden="true" />{questionId}</span>
      {question.blocking ? <span className="question-blocking-badge">{t("issues.answer.panelBlocking")}</span> : null}
    </header>
    {effective ? <div className="question-effective-answer">
      <strong>{t("issues.answer.panelEffective")}</strong>
      <span>{effective.selection.custom || effective.selection.options.map((option) => option.label).join(", ")}</span>
      <small>
        {t("issues.answer.panelEffectiveBy", { id: effective.id, actor: effective.actor })}
        {" "}<PreciseRelativeTime value={effective.created_at} now={now} />
        {effective.source_url ? <>{" · "}<a href={effective.source_url}>{t("issues.answer.panelEffectiveSource")}<ExternalLink aria-hidden="true" /></a></> : null}
      </small>
    </div> : null}
    <fieldset disabled={!canAnswer}>
      <legend>{t(effective ? "issues.answer.panelLegendAgain" : "issues.answer.panelLegend")}</legend>
      {model.options.map((option) => <label key={option.id} className="question-option">
        <input
          type={model.mode === "single" ? "radio" : "checkbox"}
          name={`${questionId}-options`}
          value={option.id}
          checked={optionIds.includes(option.id)}
          onChange={(event) => chooseOption(option.id, event.target.checked)}
        />
        <span>
          <span className="question-option-label">{option.label}</span>
          {option.description ? <span className="question-option-note">{option.description}</span> : null}
          {option.tradeoff ? <span className="question-option-note">{option.tradeoff}</span> : null}
        </span>
      </label>)}
      {model.allow_custom ? <label className="question-custom">
        {t("issues.answer.panelCustom")}
        <textarea maxLength={4096} value={custom} onChange={(event) => editCustom(event.target.value)} />
      </label> : null}
      <div className="question-panel-actions">
        <button className="issue-button primary" type="button" disabled={!canAnswer || !hasSelection || mutation.isPending} onClick={submit}>
          {t(mutation.isPending ? "issues.answer.submitting" : "issues.answer.panelSubmit")}
        </button>
        <small>{t(canAnswer ? "issues.answer.panelTrust" : "issues.answer.panelReadOnly")}</small>
      </div>
    </fieldset>
    {mutation.error ? <p className="question-panel-status" role="alert">{t("issues.answer.failed")}</p> : null}
  </section>;
}
