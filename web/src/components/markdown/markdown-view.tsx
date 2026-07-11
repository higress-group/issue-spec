import ReactMarkdown from "react-markdown";
import rehypeHighlight from "rehype-highlight";
import rehypeRaw from "rehype-raw";
import rehypeSanitize, { defaultSchema } from "rehype-sanitize";
import remarkGfm from "remark-gfm";
import { stripIssueSpecMarkersForRender } from "./issue-markers";
import "highlight.js/styles/github.css";
import "./markdown.css";

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

export function MarkdownView({ source, className = "" }: { source: string; className?: string }) {
  return <div className={`markdown-view ${className}`.trim()} data-testid="rendered-markdown">
    <ReactMarkdown
      remarkPlugins={[remarkGfm]}
      rehypePlugins={[rehypeRaw, [rehypeSanitize, schema], rehypeHighlight]}
      components={{
        a: ({ href, children, node, ...props }) => {
          void node;
          return <a {...props} href={href} target="_blank" rel="noopener noreferrer" referrerPolicy="no-referrer">{children}</a>;
        },
        img: ({ node, ...props }) => {
          void node;
          return <img {...props} alt={props.alt ?? ""} loading="lazy" referrerPolicy="no-referrer" />;
        },
        input: ({ node, ...props }) => {
          void node;
          return <input {...props} aria-label={props.checked ? "Completed task" : "Incomplete task"} />;
        },
        pre: ({ node, ...props }) => {
          void node;
          return <pre {...props} tabIndex={0} aria-label="Code block" />;
        },
        code: ({ node, className, ...props }) => {
          void node;
          const block = className?.includes("language-") || className?.includes("hljs");
          return <code {...props} className={className} tabIndex={block ? 0 : undefined} aria-label={block ? "Highlighted code" : undefined} />;
        },
      }}
    >{stripIssueSpecMarkersForRender(source)}</ReactMarkdown>
  </div>;
}
