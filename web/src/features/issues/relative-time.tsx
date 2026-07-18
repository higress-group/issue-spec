import { useEffect, useState } from "react";
import { useTranslation } from "react-i18next";

type RelativeTimeUnit = "second" | "minute" | "hour" | "day" | "month" | "year";

const relativeTimeScales: ReadonlyArray<{ limit: number; seconds: number; unit: RelativeTimeUnit }> = [
  { limit: 60, seconds: 1, unit: "second" },
  { limit: 60 * 60, seconds: 60, unit: "minute" },
  { limit: 24 * 60 * 60, seconds: 60 * 60, unit: "hour" },
  { limit: 30 * 24 * 60 * 60, seconds: 24 * 60 * 60, unit: "day" },
  { limit: 365 * 24 * 60 * 60, seconds: 30 * 24 * 60 * 60, unit: "month" },
  { limit: Number.POSITIVE_INFINITY, seconds: 365 * 24 * 60 * 60, unit: "year" },
];

function timestampValue(value: string | number | Date) {
  const date = value instanceof Date ? value : new Date(value);
  const timestamp = date.valueOf();
  return Number.isFinite(timestamp) ? timestamp : null;
}

export function formatRelativeTime(value: string, now: number | Date, locale: string) {
  const timestamp = timestampValue(value);
  const nowTimestamp = timestampValue(now);
  if (timestamp === null || nowTimestamp === null) return null;

  const differenceInSeconds = (timestamp - nowTimestamp) / 1_000;
  const scale = relativeTimeScales.find(({ limit }) => Math.abs(differenceInSeconds) < limit) ?? relativeTimeScales.at(-1)!;
  const amount = Math.trunc(differenceInSeconds / scale.seconds);
  try {
    return new Intl.RelativeTimeFormat(locale, { numeric: "always", style: "narrow" }).format(amount, scale.unit);
  } catch {
    return null;
  }
}

export function formatPreciseTime(value: string, locale: string) {
  const timestamp = timestampValue(value);
  if (timestamp === null) return null;
  try {
    return new Intl.DateTimeFormat(locale, {
      year: "numeric",
      month: "short",
      day: "numeric",
      hour: "2-digit",
      minute: "2-digit",
      second: "2-digit",
      timeZoneName: "short",
    }).format(timestamp);
  } catch {
    return null;
  }
}

export function isLaterTimestamp(candidate: string, baseline: string) {
  const candidateTimestamp = timestampValue(candidate);
  const baselineTimestamp = timestampValue(baseline);
  return candidateTimestamp !== null && baselineTimestamp !== null && candidateTimestamp > baselineTimestamp;
}

export function useSecondClock() {
  const [now, setNow] = useState(() => Date.now());

  useEffect(() => {
    const interval = window.setInterval(() => setNow(Date.now()), 1_000);
    return () => window.clearInterval(interval);
  }, []);

  return now;
}

export function PreciseRelativeTime({ value, now }: { value: string; now: number | Date }) {
  const { t, i18n } = useTranslation();
  const locale = i18n.resolvedLanguage ?? i18n.language;
  const relative = formatRelativeTime(value, now, locale);
  const precise = formatPreciseTime(value, locale);
  const timestamp = timestampValue(value);

  if (relative === null || precise === null || timestamp === null) {
    const fallback = value.trim() || t("issues.time.unknown");
    return <span title={fallback}>{fallback}</span>;
  }

  return <time dateTime={new Date(timestamp).toISOString()} title={precise} aria-label={t("issues.time.accessible", { relative, precise })}>{relative}</time>;
}
