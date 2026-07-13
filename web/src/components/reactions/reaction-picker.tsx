import { useMutation, useQuery, useQueryClient } from "@tanstack/react-query";
import { issueApi } from "../../features/issues/api";
import type { ReactionContent, Reactions } from "../../features/issues/types";
import { useTranslation } from "react-i18next";
import "./reactions.css";

const reactionChoices: { content: ReactionContent; emoji: string; labelKey: string }[] = [
  { content: "+1", emoji: "👍", labelKey: "thumbsUp" }, { content: "-1", emoji: "👎", labelKey: "thumbsDown" },
  { content: "laugh", emoji: "😄", labelKey: "laugh" }, { content: "hooray", emoji: "🎉", labelKey: "hooray" },
  { content: "confused", emoji: "😕", labelKey: "confused" }, { content: "heart", emoji: "❤️", labelKey: "heart" },
  { content: "rocket", emoji: "🚀", labelKey: "rocket" }, { content: "eyes", emoji: "👀", labelKey: "eyes" },
];

export function ReactionPicker({ owner, repo, commentId, summary, currentLogin, readOnly = false }: { owner: string; repo: string; commentId: number; summary: Reactions; currentLogin: string; readOnly?: boolean }) {
  const { t } = useTranslation();
  const queryClient = useQueryClient();
  const key = ["issues", owner, repo, "comment", commentId, "reactions"] as const;
  const reactions = useQuery({ queryKey: key, queryFn: ({ signal }) => issueApi.listReactions(owner, repo, commentId, signal), enabled: !readOnly });
  const mutation = useMutation({
    mutationFn: async (content: ReactionContent) => {
      const mine = reactions.data?.find((reaction) => reaction.user.login === currentLogin && reaction.content === content);
      if (mine) return issueApi.deleteReaction(owner, repo, commentId, mine.id);
      return issueApi.createReaction(owner, repo, commentId, content);
    },
    onSuccess: async () => {
      await queryClient.invalidateQueries({ queryKey: key });
      await queryClient.invalidateQueries({ queryKey: ["issues", owner, repo] });
    },
  });
  if (readOnly) return <div className="reaction-bar read-only" aria-label={t("issues.reactions.aria")}>{reactionChoices.filter((choice) => summary[choice.content] > 0).map((choice) => { const label = t(`issues.reactions.${choice.labelKey}`); return <span key={choice.content} aria-label={t("issues.reactions.summary", { label, count: summary[choice.content] })}><span aria-hidden="true">{choice.emoji}</span>{summary[choice.content]}</span>; })}</div>;
  return <div className="reaction-bar" aria-label={t("issues.reactions.aria")}>{reactionChoices.map((choice) => {
    const count = summary[choice.content];
    const mine = reactions.data?.some((reaction) => reaction.user.login === currentLogin && reaction.content === choice.content) ?? false;
    const label = t(`issues.reactions.${choice.labelKey}`);
    return <button key={choice.content} type="button" className={mine ? "mine" : ""} aria-pressed={mine} aria-label={t("issues.reactions.action", { action: t(mine ? "issues.reactions.remove" : "issues.reactions.add"), label, countText: count ? t("issues.reactions.countText", { count }) : "" })} disabled={mutation.isPending} onClick={() => mutation.mutate(choice.content)}><span aria-hidden="true">{choice.emoji}</span>{count > 0 ? <span>{count}</span> : null}</button>;
  })}{mutation.error ? <span className="reaction-error" role="status">{t("issues.reactions.saveError")}</span> : null}</div>;
}
