import { memo, useCallback, useEffect, useRef, useState } from "react";
import { ChevronDown, ChevronRight, Play, RefreshCw, Square } from "lucide-react";
import { useTranslation } from "react-i18next";
import { parsePreviewAnswerMessage, type PreviewAnswerIntent } from "./html-preview-message";
import "./html-preview.css";

const previewLifetimeMs = 10 * 60 * 1_000;
const iframeAllow = [
  "accelerometer 'none'", "autoplay 'none'", "camera 'none'", "display-capture 'none'",
  "encrypted-media 'none'", "fullscreen 'none'", "geolocation 'none'", "gyroscope 'none'",
  "magnetometer 'none'", "microphone 'none'", "midi 'none'", "payment 'none'",
  "picture-in-picture 'none'", "publickey-credentials-get 'none'", "screen-wake-lock 'none'",
  "usb 'none'", "web-share 'none'", "xr-spatial-tracking 'none'",
].join("; ");

export type HtmlPreviewDescriptor = {
  id: string;
  title: string;
  height: number;
  source: string;
};

export type HtmlPreviewActivity = {
  claim: (key: string) => boolean;
  release: (key: string) => void;
};

export type HtmlPreviewContext = {
  sourceKey: string;
  previewURL: (id: string, digest: string) => string;
  activity: HtmlPreviewActivity;
  answersEnabled: boolean;
  onAnswerIntent?: (intent: PreviewAnswerIntent) => void;
};

type State = "stopped" | "preparing" | "running" | "error";

export function createHtmlPreviewActivity(limit = 2): HtmlPreviewActivity {
  const active = new Set<string>();
  return {
    claim(key) {
      if (active.has(key)) return true;
      if (active.size >= limit) return false;
      active.add(key);
      return true;
    },
    release(key) {
      active.delete(key);
    },
  };
}

async function sourceDigest(source: string) {
  const bytes = new TextEncoder().encode(source);
  const digest = await globalThis.crypto.subtle.digest("SHA-256", bytes);
  return [...new Uint8Array(digest)].map((value) => value.toString(16).padStart(2, "0")).join("");
}

function randomNonce() {
  const bytes = new Uint8Array(24);
  globalThis.crypto.getRandomValues(bytes);
  return [...bytes].map((value) => value.toString(16).padStart(2, "0")).join("");
}

export const HtmlPreview = memo(function HtmlPreview({
  descriptor,
  context,
}: {
  descriptor: HtmlPreviewDescriptor;
  context: HtmlPreviewContext;
}) {
  const { t } = useTranslation();
  const [expanded, setExpanded] = useState(false);
  const [state, setState] = useState<State>("stopped");
  const [mount, setMount] = useState<{ digest: string; nonce: string; generation: number } | null>(null);
  const iframeRef = useRef<HTMLIFrameElement>(null);
  const previousSource = useRef(descriptor.source);
  const alive = useRef(true);
  const runSerial = useRef(0);
  const lifetime = useRef<ReturnType<typeof setTimeout> | null>(null);
  const { activity, answersEnabled, onAnswerIntent, previewURL } = context;
  const key = `${context.sourceKey}:${descriptor.id}`;

  const clearLifetime = useCallback(() => {
    if (lifetime.current) clearTimeout(lifetime.current);
    lifetime.current = null;
  }, []);
  const stop = useCallback(() => {
    runSerial.current += 1;
    clearLifetime();
    activity.release(key);
    setMount(null);
    setState("stopped");
  }, [activity, clearLifetime, key]);
  const armLifetime = useCallback(() => {
    clearLifetime();
    lifetime.current = setTimeout(stop, previewLifetimeMs);
  }, [clearLifetime, stop]);
  const run = useCallback(async (reload = false) => {
    const serial = ++runSerial.current;
    setState("preparing");
    try {
      const digest = await sourceDigest(descriptor.source);
      if (!alive.current || serial !== runSerial.current) return;
      if (!activity.claim(key)) {
        setState("error");
        return;
      }
      setMount((current) => ({
        digest,
        nonce: randomNonce(),
        generation: reload ? (current?.generation ?? 0) + 1 : (current?.generation ?? 0),
      }));
      setState("running");
      armLifetime();
    } catch {
      activity.release(key);
      setState("error");
    }
  }, [activity, armLifetime, descriptor.source, key]);

  useEffect(() => {
    alive.current = true;
    return () => {
      alive.current = false;
      runSerial.current += 1;
      clearLifetime();
      activity.release(key);
    };
  }, [activity, clearLifetime, key]);

  useEffect(() => {
    if (previousSource.current === descriptor.source) return;
    previousSource.current = descriptor.source;
    stop();
  }, [descriptor.source, stop]);

  useEffect(() => {
    if (!mount || !answersEnabled || !onAnswerIntent) return;
    const receive = (event: MessageEvent) => {
      const intent = parsePreviewAnswerMessage(event, iframeRef.current?.contentWindow ?? null, mount.nonce);
      if (!intent) return;
      armLifetime();
      onAnswerIntent(intent);
    };
    window.addEventListener("message", receive);
    return () => window.removeEventListener("message", receive);
  }, [answersEnabled, armLifetime, mount, onAnswerIntent]);

  const toggle = () => {
    if (expanded) stop();
    setExpanded((value) => !value);
  };
  const title = descriptor.title || t("markdown.preview.defaultTitle", { id: descriptor.id });
  return <section className="html-preview" data-preview-id={descriptor.id}>
    <header>
      <button type="button" className="html-preview-disclosure" aria-expanded={expanded} onClick={toggle}>
        {expanded ? <ChevronDown aria-hidden="true" /> : <ChevronRight aria-hidden="true" />}
        <span><strong>{title}</strong><small>{t("markdown.preview.reviewSurface")}</small></span>
      </button>
      <span className={`html-preview-state ${state}`}>{t(`markdown.preview.state.${state}`)}</span>
    </header>
    {expanded ? <div className="html-preview-body">
      <p>{t(answersEnabled ? "markdown.preview.securityAndAnswers" : "markdown.preview.securityOnly")}</p>
      <div className="html-preview-actions">
        {!mount ? <button type="button" onClick={() => void run(false)} disabled={state === "preparing"}><Play aria-hidden="true" />{t("markdown.preview.run")}</button> : <>
          <button type="button" onClick={() => void run(true)}><RefreshCw aria-hidden="true" />{t("markdown.preview.reload")}</button>
          <button type="button" onClick={stop}><Square aria-hidden="true" />{t("markdown.preview.stop")}</button>
        </>}
      </div>
      {state === "error" ? <p className="html-preview-error" role="alert">{t("markdown.preview.activeLimit")}</p> : null}
      {mount ? <iframe
        key={`${mount.digest}:${mount.generation}`}
        ref={iframeRef}
        title={title}
        src={previewURL(descriptor.id, mount.digest)}
        height={descriptor.height}
        sandbox="allow-scripts"
        referrerPolicy="no-referrer"
        allow={iframeAllow}
        loading="eager"
        onLoad={() => iframeRef.current?.contentWindow?.postMessage({
          version: 1,
          type: "issue-spec-preview-init",
          nonce: mount.nonce,
          interactive_question_answers: answersEnabled,
        }, "*")}
      /> : null}
    </div> : null}
  </section>;
});

export { iframeAllow };
