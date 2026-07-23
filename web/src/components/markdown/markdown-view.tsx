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

export const MarkdownView = memo(function MarkdownView({ source, className = "" }: { source: string; className?: string }) {
  const { t } = useTranslation();
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
    pre: ({ node, children, ...props }: React.ComponentProps<"pre"> & { node?: unknown }) => {
      void node;
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
  }), [t]);
  return <div className={`markdown-view ${className}`.trim()} data-testid="rendered-markdown">
    <ReactMarkdown
      remarkPlugins={[remarkGfm, remarkMentions]}
      rehypePlugins={[rehypeRaw, [rehypeSanitize, schema], rehypeHighlight]}
      components={components}
    >{stripIssueSpecMarkersForRender(source)}</ReactMarkdown>
  </div>;
});
