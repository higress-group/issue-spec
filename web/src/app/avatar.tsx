import { useMemo, useState, type CSSProperties } from "react";

type AvatarProps = {
  login: string;
  displayName?: string;
  src?: string | null;
  size?: number;
  tone?: "default" | "coral" | "inverse";
  className?: string;
};

export function Avatar({ login, displayName, src, size = 44, tone = "default", className = "" }: AvatarProps) {
  const safeSource = useMemo(() => safeAvatarSource(src), [src]);
  const [failedSource, setFailedSource] = useState("");
  const label = displayName?.trim() || login.trim() || "Account";
  const initials = avatarInitials(displayName, login);
  const style = { "--avatar-size": `${size}px` } as CSSProperties;
  return <span className={`user-avatar ${tone} ${className}`.trim()} style={style} role="img" aria-label={`${label} avatar`}>
    {safeSource && failedSource !== safeSource ? <img src={safeSource} alt="" loading="lazy" decoding="async" width={size} height={size} onError={() => setFailedSource(safeSource)} /> : <span aria-hidden="true">{initials}</span>}
  </span>;
}

export function safeAvatarSource(raw?: string | null) {
  if (!raw || typeof window === "undefined") return "";
  try {
    const candidate = new URL(raw, window.location.origin);
    if (candidate.origin !== window.location.origin || candidate.username || candidate.password || candidate.search || candidate.hash) return "";
    if (!candidate.pathname.startsWith("/api/v1/avatars/") || candidate.pathname.slice("/api/v1/avatars/".length).includes("/")) return "";
    return candidate.href;
  } catch {
    return "";
  }
}

export function avatarInitials(displayName?: string, login = "") {
  const words = (displayName ?? "").trim().split(/\s+/).filter(Boolean);
  const value = words.length > 1 ? `${words[0][0]}${words.at(-1)?.[0] ?? ""}` : (words[0] ?? login).slice(0, 2);
  return value.toLocaleUpperCase() || "?";
}
