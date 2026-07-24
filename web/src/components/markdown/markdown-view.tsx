import { isValidElement, memo, type ReactNode, useMemo } from "react";
import ReactMarkdown from "react-markdown";
import rehypeHighlight from "rehype-highlight";
import rehypeRaw from "rehype-raw";
import rehypeSanitize, { defaultSchema } from "rehype-sanitize";
import remarkGfm from "remark-gfm";
import { stripIssueSpecMarkersForRender } from "./issue-markers";
import "highlight.js/styles/github.css";
import "./markdown.css";
import { useTranslation } from "react-i18next";
import { remarkMentions } from "./mentions";
import { MermaidDiagram } from "./mermaid-diagram";
import { HtmlPreview, type HtmlPreviewContext, type HtmlPreviewDescriptor } from "./html-preview";

const schema = {
  ...defaultSchema,
  tagNames: (defaultSchema.tagNames ?? []).filter((tag) => !["iframe", "svg", "style", "script"].includes(tag)),
  attributes: {
    ...defaultSchema.attributes,
    code: [...(defaultSchema.attributes?.code ?? []), ["className", /^language-[A-Za-z0-9_-]+$/, /^hljs(?:-[A-Za-z0-9_-]+)?$/]],
    input: [["type", "checkbox"], ["checked", true], ["disabled", true]],
  },
  protocols: {
    ...defaultSchema.protocols,
    href: ["http", "https", "mailto"],
    src: ["http", "https"],
  },
};

const commentAnchorPattern = /^#issuecomment-[1-9]\d*$/;

function isSamePageCommentLink(href: string | undefined) {
  if (!href || typeof window === "undefined") return false;
  try {
    const current = new URL(window.location.href);
    const candidate = new URL(href, current);
    const normalizePath = (path: string) => (path.replace(/\/+$/, "") || "/").toLowerCase();
    return candidate.origin === current.origin
      && normalizePath(candidate.pathname) === normalizePath(current.pathname)
      && commentAnchorPattern.test(candidate.hash);
  } catch {
    return false;
  }
}

function isSameOriginUserLink(href: string | undefined) {
  if (!href || typeof window === "undefined") return false;
  try {
    const candidate = new URL(href, window.location.origin);
    return candidate.origin === window.location.origin && /^\/users\/[^/]+$/.test(candidate.pathname)
      && candidate.search === "" && candidate.hash === "";
  } catch {
    return false;
  }
}

function mermaidSource(children: ReactNode) {
  if (!isValidElement<{ className?: string; children?: ReactNode }>(children)) return null;
  const language = children.props.className?.split(/\s+/);
  if (!language?.includes("language-mermaid")) return null;
  const source = children.props.children;
  if (typeof source !== "string") return null;
  return source.replace(/\n$/, "");
}

type LineRange = { start: number; contentEnd: number; end: number };
type Opener = { char: "`" | "~"; length: number; preview: boolean; metadata: string };
type ParsedPreview = HtmlPreviewDescriptor & { start: number; diagnostics: string[] };

function sourceLines(source: string): LineRange[] {
  const lines: LineRange[] = [];
  for (let start = 0; start < source.length;) {
    const newline = source.indexOf("\n", start);
    if (newline < 0) {
      lines.push({ start, contentEnd: source.length, end: source.length });
      break;
    }
    const end = newline + 1;
    const contentEnd = newline > start && source[newline - 1] === "\r" ? newline - 1 : newline;
    lines.push({ start, contentEnd, end });
    start = end;
  }
  return lines;
}

function fenceOpener(line: string): Opener | null {
  let index = 0;
  while (index < line.length && line[index] === " " && index < 4) index++;
  if (index > 3 || (line[index] !== "`" && line[index] !== "~")) return null;
  const char = line[index] as "`" | "~";
  let runEnd = index;
  while (line[runEnd] === char) runEnd++;
  if (runEnd - index < 3) return null;
  const info = line.slice(runEnd);
  if (char === "`" && info.includes("`")) return null;
  const trimmed = info.trim();
  const separator = trimmed.search(/[ \t]/);
  const language = separator < 0 ? trimmed : trimmed.slice(0, separator);
  return {
    char,
    length: runEnd - index,
    preview: language === "html-preview",
    metadata: separator < 0 ? "" : trimmed.slice(separator).trim(),
  };
}

function closesFence(line: string, opener: Opener) {
  let index = 0;
  while (index < line.length && line[index] === " " && index < 4) index++;
  if (index > 3 || line[index] !== opener.char) return false;
  let runEnd = index;
  while (line[runEnd] === opener.char) runEnd++;
  return runEnd - index >= opener.length && /^[ \t]*$/.test(line.slice(runEnd));
}

function previewMetadata(raw: string) {
  const values = new Map<string, string>();
  if (new TextEncoder().encode(raw).byteLength > 4_096) return null;
  for (let index = 0; index < raw.length;) {
    while (raw[index] === " " || raw[index] === "\t") index++;
    if (index === raw.length) return values;
    const keyStart = index;
    while (/[A-Za-z0-9_-]/.test(raw[index] ?? "")) index++;
    if (keyStart === index || raw[index] !== "=") return null;
    const key = raw.slice(keyStart, index++);
    if (values.has(key) || index === raw.length) return null;
    let value = "";
    if (raw[index] === "\"" || raw[index] === "'") {
      const quote = raw[index++];
      let closed = false;
      while (index < raw.length) {
        if (raw[index] === quote) {
          index++;
          closed = true;
          break;
        }
        if (raw[index] === "\\") {
          if (raw[index + 1] !== quote && raw[index + 1] !== "\\") return null;
          value += raw[index + 1];
          index += 2;
        } else {
          value += raw[index++];
        }
      }
      if (!closed || (index < raw.length && raw[index] !== " " && raw[index] !== "\t")) return null;
    } else {
      const valueStart = index;
      while (index < raw.length && raw[index] !== " " && raw[index] !== "\t") index++;
      value = raw.slice(valueStart, index);
    }
    if (!value && key !== "title") return null;
    values.set(key, value);
  }
  return values;
}

function parseHtmlPreviews(source: string) {
  const lines = sourceLines(source);
  const previews: ParsedPreview[] = [];
  for (let index = 0; index < lines.length;) {
    const line = lines[index];
    const opener = fenceOpener(source.slice(line.start, line.contentEnd));
    if (!opener) {
      index++;
      continue;
    }
    let closeLine = -1;
    for (let candidate = index + 1; candidate < lines.length; candidate++) {
      if (closesFence(source.slice(lines[candidate].start, lines[candidate].contentEnd), opener)) {
        closeLine = candidate;
        break;
      }
    }
    if (!opener.preview) {
      if (closeLine < 0) break;
      index = closeLine + 1;
      continue;
    }
    const diagnostics: string[] = [];
    const metadata = previewMetadata(opener.metadata);
    const id = metadata?.get("id") ?? "";
    const version = metadata?.get("version") ?? "";
    const unknown = metadata ? [...metadata.keys()].some((key) => !["id", "version", "title", "height"].includes(key)) : true;
    if (!metadata || unknown || !/^[a-z][a-z0-9-]{0,63}$/.test(id) || version !== "1") diagnostics.push("metadata");
    let height = 480;
    const heightText = metadata?.get("height");
    if (heightText) {
      if (!/^-?[0-9]+$/.test(heightText)) diagnostics.push("height");
      else height = Math.max(240, Math.min(720, Number(heightText)));
    }
    const title = metadata?.get("title") ?? "";
    if ([...title].length > 120) diagnostics.push("title");
    const sourceStart = line.end;
    const sourceEnd = closeLine < 0 ? source.length : lines[closeLine].start;
    const previewSource = source.slice(sourceStart, sourceEnd);
    if (closeLine < 0) diagnostics.push("unclosed");
    if (new TextEncoder().encode(previewSource).byteLength > 256 * 1_024) diagnostics.push("size");
    previews.push({ start: line.start, id, title, height, source: previewSource, diagnostics });
    if (closeLine < 0) break;
    index = closeLine + 1;
  }
  const counts = new Map<string, number>();
  for (const preview of previews) counts.set(preview.id, (counts.get(preview.id) ?? 0) + 1);
  if (previews.length > 8) previews.forEach((preview) => preview.diagnostics.push("count"));
  previews.forEach((preview) => {
    if ((counts.get(preview.id) ?? 0) > 1) preview.diagnostics.push("duplicate");
  });
  return new Map(previews.filter((preview) => preview.diagnostics.length === 0).map((preview) => [preview.start, preview]));
}

export function hasExecutableHtmlPreview(source: string) {
  return parseHtmlPreviews(stripIssueSpecMarkersForRender(source)).size > 0;
}

type MarkdownViewProps = {
  source: string;
  className?: string;
  previewContext?: HtmlPreviewContext;
  expandFirstPreview?: boolean;
};

export const MarkdownView = memo(function MarkdownView({
  source,
  className = "",
  previewContext,
  expandFirstPreview = false,
}: MarkdownViewProps) {
  const { t } = useTranslation();
  const renderedSource = useMemo(() => stripIssueSpecMarkersForRender(source), [source]);
  const htmlPreviews = useMemo(() => parseHtmlPreviews(renderedSource), [renderedSource]);
  const firstPreviewOffset = expandFirstPreview ? htmlPreviews.keys().next().value : undefined;
  const components = useMemo(() => ({
    a: ({ href, children, node, ...props }: React.ComponentProps<"a"> & { node?: unknown }) => {
      void node;
      const internal = isSamePageCommentLink(href) || isSameOriginUserLink(href);
      return <a {...props} href={href} target={internal ? undefined : "_blank"} rel={internal ? undefined : "noopener noreferrer"} referrerPolicy="no-referrer">{children}</a>;
    },
    img: ({ node, ...props }: React.ComponentProps<"img"> & { node?: unknown }) => {
      void node;
      return <img {...props} alt={props.alt ?? ""} loading="lazy" referrerPolicy="no-referrer" />;
    },
    input: ({ node, ...props }: React.ComponentProps<"input"> & { node?: unknown }) => {
      void node;
      return <input {...props} aria-label={t(props.checked ? "markdown.completedTask" : "markdown.incompleteTask")} />;
    },
    pre: ({ node, children, ...props }: React.ComponentProps<"pre"> & { node?: { position?: { start?: { offset?: number } } } }) => {
      const offset = node?.position?.start?.offset ?? -1;
      const preview = htmlPreviews.get(offset);
      if (preview && previewContext) return <HtmlPreview
        descriptor={preview}
        context={previewContext}
        defaultExpanded={offset === firstPreviewOffset}
      />;
      const diagram = mermaidSource(children);
      return diagram === null
        ? <pre {...props} tabIndex={0} aria-label={t("markdown.codeBlock")}>{children}</pre>
        : <MermaidDiagram source={diagram} />;
    },
    code: ({ node, className: codeClassName, ...props }: React.ComponentProps<"code"> & { node?: unknown }) => {
      void node;
      const block = codeClassName?.includes("language-") || codeClassName?.includes("hljs");
      return <code {...props} className={codeClassName} tabIndex={block ? 0 : undefined} aria-label={block ? t("markdown.highlightedCode") : undefined} />;
    },
  }), [firstPreviewOffset, htmlPreviews, previewContext, t]);
  return <div className={`markdown-view ${className}`.trim()} data-testid="rendered-markdown">
    <ReactMarkdown
      remarkPlugins={[remarkGfm, remarkMentions]}
      rehypePlugins={[rehypeRaw, [rehypeSanitize, schema], rehypeHighlight]}
      components={components}
    >{renderedSource}</ReactMarkdown>
  </div>;
});
