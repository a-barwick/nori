import { fromMarkdown } from "mdast-util-from-markdown";
import { gfmFromMarkdown } from "mdast-util-gfm";
import { gfm } from "micromark-extension-gfm";
import type { List, Nodes, Paragraph, Root, RootContent } from "mdast";

/** Pure markdown presentation: short replies are bubbles, structured replies are documents. */
const PAGE_WORDS = 150;
const MAX_BUBBLE_LIST_ITEMS = 5;
const MAX_BUBBLE_CODE_LINES = 3;

export function extractHints(markdown: string): { body: string; hints: { mode?: "page" | "bubble" } } {
  const hints: { mode?: "page" | "bubble" } = {};
  const body = markdown.replace(/<!--\s*nori:([a-z]+)[\s\S]*?-->/g, (_match, name: string) => {
    if (name === "page" || name === "bubble") hints.mode = name;
    return "";
  }).replace(/<!--(?![\s\S]*-->)[\s\S]*$/, "");
  return { body: body.trim(), hints };
}

function parse(markdown: string): Root {
  return fromMarkdown(markdown, { extensions: [gfm()], mdastExtensions: [gfmFromMarkdown()] });
}

function textOf(node: Nodes): string {
  if ("value" in node && typeof node.value === "string") return node.value;
  if ("children" in node) return (node.children as Nodes[]).map(textOf).join(" ");
  return "";
}


function isSourceList(list: List, prev?: RootContent) {
  const prevText = prev ? textOf(prev).trim().toLowerCase() : "";
  if (/^(sources?|references|links)\b/.test(prevText)) return true;
  if (list.children.length < 2) return false;
  return list.children.every((item) => {
    const first = item.children[0];
    if (!first || first.type !== "paragraph") return false;
    return first.children[0]?.type === "link";
  });
}

function structureReasons(tree: Root): string[] {
  const reasons = new Set<string>();
  const visit = (node: Nodes, depth: number, siblings?: RootContent[], index?: number) => {
    switch (node.type) {
      case "heading":
        reasons.add("heading");
        break;
      case "table":
        reasons.add("table");
        break;
      case "code":
        if (node.value.split("\n").length > MAX_BUBBLE_CODE_LINES) reasons.add("code block");
        break;
      case "list": {
        if (depth > 0) reasons.add("nested list");
        if (node.children.length > MAX_BUBBLE_LIST_ITEMS) reasons.add("long list");
        if (siblings && index !== undefined && isSourceList(node, siblings[index - 1])) reasons.add("source list");
        node.children.forEach((item) => item.children.forEach((c) => visit(c, depth + 1)));
        return;
      }
    }
    if ("children" in node && node.type !== "listItem") {
      const kids = node.children as RootContent[];
      kids.forEach((c, i) => visit(c, depth, kids, i));
    }
  };
  tree.children.forEach((c, i) => visit(c, 0, tree.children, i));
  return [...reasons];
}

interface Classified {
  mode: "bubble" | "page";
  /** The gist bubble that always precedes a page. */
  cover?: string;
  /** Bubble text, or the page body. */
  body: string;
  reasons: string[];
}

const FALLBACK_COVER = "Here's the full write-up.";

function isGist(p: Paragraph, source: string) {
  const text = source.slice(p.position!.start.offset, p.position!.end.offset);
  const sentences = text.split(/(?<=[.!?])\s+/).filter(Boolean).length;
  return sentences <= 2 && text.split(/\s+/).length <= 45;
}

export function classify(markdown: string, forced?: "page" | "bubble"): Classified {
  const tree = parse(markdown);
  const reasons = structureReasons(tree);
  const words = textOf(tree).split(/\s+/).filter((w) => /\w/.test(w)).length;
  if (words > PAGE_WORDS) reasons.push(`${words} words`);

  const mode = forced ?? (reasons.length ? "page" : "bubble");
  if (mode === "bubble") return { mode, body: markdown, reasons };

  const first = tree.children[0];
  if (first?.type === "paragraph" && tree.children.length > 1 && isGist(first, markdown)) {
    const cover = markdown.slice(first.position!.start.offset, first.position!.end.offset).trim();
    const body = markdown.slice(first.position!.end.offset).trim();
    return { mode, cover, body, reasons };
  }
  // One long opening paragraph: its first sentence is the gist.
  if (first?.type === "paragraph" && first.position) {
    const start = first.position.start.offset ?? 0;
    const text = markdown.slice(start, first.position.end.offset);
    const sentence = /^[^\n]*?[.!?](?=\s)/.exec(text)?.[0];
    if (sentence && sentence.split(/\s+/).length <= 30) {
      return { mode, cover: sentence.trim(), body: markdown.slice(start + sentence.length).trim(), reasons };
    }
  }
  return { mode, cover: FALLBACK_COVER, body: markdown, reasons };
}
