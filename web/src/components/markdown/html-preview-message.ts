export type PreviewAnswerIntent = {
  questionId: string;
  mode: "single" | "multiple";
  optionIds: string[];
  custom: string;
};

const questionIDPattern = /^QUESTION-[0-9]{3,}$/;
const optionIDPattern = /^[a-z][a-z0-9-]{0,63}$/;
const messageKeys = ["custom", "mode", "nonce", "option_ids", "question_id", "version"];
const encoder = new TextEncoder();
const maxAnswerRequestBytes = 16 * 1_024;
const digestBudget = "0".repeat(64);

function scalarLength(value: string) {
  return [...value].length;
}

export function previewAnswerRequest(intent: PreviewAnswerIntent, questionDigest: string) {
  return {
    question_id: intent.questionId,
    question_digest: questionDigest,
    option_ids: intent.optionIds,
    custom: intent.custom,
  };
}

export function parsePreviewAnswerMessage(
  event: Pick<MessageEvent, "data" | "origin" | "source">,
  expectedSource: MessageEventSource | null,
  expectedNonce: string,
): PreviewAnswerIntent | null {
  if (!expectedSource || event.source !== expectedSource || event.origin !== "null") return null;
  if (!event.data || typeof event.data !== "object" || Array.isArray(event.data)) return null;
  const value = event.data as Record<string, unknown>;
  if (Object.keys(value).sort().join("\n") !== messageKeys.join("\n")) return null;
  if (value.version !== 1 || value.nonce !== expectedNonce) return null;
  if (typeof value.question_id !== "string" || !questionIDPattern.test(value.question_id)) return null;
  if (value.mode !== "single" && value.mode !== "multiple") return null;
  if (!Array.isArray(value.option_ids) || value.option_ids.length > 20) return null;
  if (typeof value.custom !== "string" || scalarLength(value.custom) > 4_096) return null;

  const optionIds: string[] = [];
  const seen = new Set<string>();
  for (const option of value.option_ids) {
    if (typeof option !== "string" || !optionIDPattern.test(option) || seen.has(option)) return null;
    seen.add(option);
    optionIds.push(option);
  }
  const hasCustom = value.custom.trim() !== "";
  if (hasCustom === (optionIds.length > 0)) return null;
  if (!hasCustom && optionIds.length === 0) return null;
  if (value.mode === "single" && optionIds.length > 1) return null;

  const intent: PreviewAnswerIntent = {
    questionId: value.question_id,
    mode: value.mode,
    optionIds,
    custom: value.custom,
  };
  if (encoder.encode(JSON.stringify(previewAnswerRequest(intent, digestBudget))).byteLength > maxAnswerRequestBytes) return null;
  return intent;
}
