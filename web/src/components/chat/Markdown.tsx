"use client";

import { useState } from "react";
import ReactMarkdown, { type Components } from "react-markdown";
import remarkGfm from "remark-gfm";
import { Check, Copy } from "lucide-react";
import { cn } from "@/lib/utils";

function CodeBlock({ children, text }: { children: React.ReactNode; text: string }) {
  const [copied, setCopied] = useState(false);
  return (
    <div className="group/code relative my-3">
      <pre className="scrollbar-quiet overflow-x-auto rounded-card bg-surface p-4 font-mono text-code text-ink shadow-[inset_0_0_0_1px_var(--hairline-strong)]">
        {children}
      </pre>
      <button
        type="button"
        aria-label={copied ? "Copied" : "Copy code"}
        onClick={() => {
          void navigator.clipboard?.writeText(text);
          setCopied(true);
          setTimeout(() => setCopied(false), 1400);
        }}
        className="t-fast absolute top-2 right-2 grid size-8 place-items-center rounded-full bg-surface text-ink-2 opacity-0 shadow-pill transition-opacity group-hover/code:opacity-100 hover:text-ink focus-visible:opacity-100"
      >
        {copied ? <Check size={15} strokeWidth={1.5} /> : <Copy size={15} strokeWidth={1.5} />}
      </button>
    </div>
  );
}

const textOf = (node: React.ReactNode): string => {
  if (typeof node === "string" || typeof node === "number") return String(node);
  if (Array.isArray(node)) return node.map(textOf).join("");
  if (node && typeof node === "object" && "props" in node) return textOf((node as { props: { children?: React.ReactNode } }).props.children);
  return "";
};

const bubble = (onAccent: boolean): Components => ({
  p: ({ children }) => <p className="[&+*]:mt-2">{children}</p>,
  ul: ({ children }) => <ul className="my-2 flex list-disc flex-col gap-1 pl-5 first:mt-0 last:mb-0">{children}</ul>,
  ol: ({ children, start }) => <ol start={start} className="my-2 flex list-decimal flex-col gap-1 pl-5 first:mt-0 last:mb-0">{children}</ol>,
  strong: ({ children }) => <strong className="font-medium">{children}</strong>,
  a: ({ href, children }) => (
    <a
      href={href}
      target="_blank"
      rel="noreferrer"
      className={onAccent ? "text-on-accent underline underline-offset-2" : "text-accent-text hover:underline"}
    >
      {children}
    </a>
  ),
  code: ({ children }) => <code className="rounded-[5px] bg-fill-2/70 px-1 font-mono text-[0.9em]">{children}</code>,
  pre: ({ children }) => <pre className="my-2 font-mono text-code whitespace-pre-wrap">{children}</pre>,
});

const page: Components = {
  p: ({ children }) => <p className="my-3 first:mt-0">{children}</p>,
  h1: ({ children }) => <h2 className="mt-6 mb-2 text-h-page font-medium text-ink first:mt-0">{children}</h2>,
  h2: ({ children }) => <h2 className="mt-6 mb-2 text-h-page font-medium text-ink first:mt-0">{children}</h2>,
  h3: ({ children }) => <h3 className="mt-5 mb-1.5 text-section font-medium text-ink first:mt-0">{children}</h3>,
  h4: ({ children }) => <h4 className="mt-4 mb-1 text-row-title font-medium text-ink">{children}</h4>,
  ul: ({ children }) => <ul className="my-3 flex list-disc flex-col gap-1.5 pl-5 marker:text-ink-3">{children}</ul>,
  ol: ({ children, start }) => <ol start={start} className="my-3 flex list-decimal flex-col gap-1.5 pl-5 marker:text-ink-3">{children}</ol>,
  strong: ({ children }) => <strong className="font-semibold">{children}</strong>,
  a: ({ href, children }) => (
    <a href={href} target="_blank" rel="noreferrer" className="text-accent-text hover:underline">
      {children}
    </a>
  ),
  blockquote: ({ children }) => <blockquote className="my-3 border-l-2 border-hairline-strong pl-4 text-ink-2">{children}</blockquote>,
  hr: () => <hr className="my-6 border-hairline" />,
  table: ({ children }) => (
    <div className="scrollbar-quiet my-4 overflow-x-auto">
      <table className="w-full border-collapse text-left text-meta">{children}</table>
    </div>
  ),
  thead: ({ children }) => <thead className="text-ink-2">{children}</thead>,
  tr: ({ children }) => <tr className="border-b border-hairline last:border-0">{children}</tr>,
  th: ({ children }) => <th className="py-2 pr-4 font-medium">{children}</th>,
  td: ({ children }) => <td className="py-2 pr-4 align-top text-ink">{children}</td>,
  code: ({ className, children }) =>
    className ? (
      <code className={className}>{children}</code>
    ) : (
      <code className="rounded-[5px] bg-fill-1 px-1 py-px font-mono text-[0.875em]">{children}</code>
    ),
  pre: ({ children }) => <CodeBlock text={textOf(children)}>{children}</CodeBlock>,
};

export function Markdown({
  children,
  variant = "bubble",
  onAccent = false,
  className,
}: {
  children: string;
  variant?: "bubble" | "page";
  onAccent?: boolean;
  className?: string;
}) {
  return (
    <div className={cn(variant === "page" ? "text-read text-ink" : "text-body", className)}>
      <ReactMarkdown remarkPlugins={[remarkGfm]} components={variant === "page" ? page : bubble(onAccent)}>
        {children}
      </ReactMarkdown>
    </div>
  );
}
