import { useMutation, useQuery, useQueryClient } from "@tanstack/react-query";
import { issueApi } from "../../features/issues/api";
import type { ReactionContent, Reactions } from "../../features/issues/types";
import "./reactions.css";

const reactionChoices: { content: ReactionContent; emoji: string; label: string }[] = [
  { content: "+1", emoji: "👍", label: "Thumbs up" }, { content: "-1", emoji: "👎", label: "Thumbs down" },
  { content: "laugh", emoji: "😄", label: "Laugh" }, { content: "hooray", emoji: "🎉", label: "Hooray" },
  { content: "confused", emoji: "😕", label: "Confused" }, { content: "heart", emoji: "❤️", label: "Heart" },
  { content: "rocket", emoji: "🚀", label: "Rocket" }, { content: "eyes", emoji: "👀", label: "Eyes" },
];

export function ReactionPicker({ owner, repo, commentId, summary, currentLogin, readOnly = false }: { owner: string; repo: string; commentId: number; summary: Reactions; currentLogin: string; readOnly?: boolean }) {
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
  if (readOnly) return <div className="reaction-bar read-only" aria-label="Comment reactions">{reactionChoices.filter((choice) => summary[choice.content] > 0).map((choice) => <span key={choice.content} aria-label={`${choice.label} reactions, ${summary[choice.content]}`}><span aria-hidden="true">{choice.emoji}</span>{summary[choice.content]}</span>)}</div>;
  return <div className="reaction-bar" aria-label="Comment reactions">{reactionChoices.map((choice) => {
    const count = summary[choice.content];
    const mine = reactions.data?.some((reaction) => reaction.user.login === currentLogin && reaction.content === choice.content) ?? false;
    return <button key={choice.content} type="button" className={mine ? "mine" : ""} aria-pressed={mine} aria-label={`${mine ? "Remove" : "Add"} ${choice.label} reaction${count ? `, ${count}` : ""}`} disabled={mutation.isPending} onClick={() => mutation.mutate(choice.content)}><span aria-hidden="true">{choice.emoji}</span>{count > 0 ? <span>{count}</span> : null}</button>;
  })}{mutation.error ? <span className="reaction-error" role="status">Reaction was not saved.</span> : null}</div>;
}
