import { fromMarkdown } from "mdast-util-from-markdown";
import { gfmFromMarkdown } from "mdast-util-gfm";
import { gfm } from "micromark-extension-gfm";
import type { List, Nodes, Root, RootContent } from "mdast";

const PAGE_WORDS = 150;
const MAX_BUBBLE_LIST_ITEMS = 5;
const MAX_BUBBLE_CODE_LINES = 3;

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

function isStructured(node: Nodes, depth = 0, previous?: RootContent): boolean {
  switch (node.type) {
    case "heading":
    case "table":
      return true;
    case "code":
      return node.value.split("\n").length > MAX_BUBBLE_CODE_LINES;
    case "list":
      return depth > 0 || node.children.length > MAX_BUBBLE_LIST_ITEMS || isSourceList(node, previous) ||
        node.children.some((item) => item.children.some((child) => isStructured(child, depth + 1)));
    default:
      if (!("children" in node) || node.type === "listItem") return false;
      const children = node.children as RootContent[];
      return children.some((child, index) => isStructured(child, depth, children[index - 1]));
  }
}

type Classified = { mode: "bubble"; body: string } | { mode: "page"; cover: string; body: string };

const FALLBACK_COVER = "Here's the full write-up.";

function isGist(text: string) {
  const sentences = text.split(/(?<=[.!?])\s+/).filter(Boolean).length;
  return sentences <= 2 && text.split(/\s+/).length <= 45;
}

export function classify(markdown: string, forced?: "page" | "bubble"): Classified {
  if (forced === "bubble") return { mode: "bubble", body: markdown };
  const tree = parse(markdown);
  if (!forced && !isStructured(tree) && textOf(tree).split(/\s+/).filter((word) => /\w/.test(word)).length <= PAGE_WORDS) {
    return { mode: "bubble", body: markdown };
  }

  const first = tree.children[0];
  if (first?.type === "paragraph" && first.position) {
    const start = first.position.start.offset ?? 0;
    const end = first.position.end.offset;
    const text = markdown.slice(start, end);
    if (tree.children.length > 1 && isGist(text)) {
      return { mode: "page", cover: text.trim(), body: markdown.slice(end).trim() };
    }
    const sentence = /^[^\n]*?[.!?](?=\s)/.exec(text)?.[0];
    if (sentence && sentence.split(/\s+/).length <= 30) {
      return { mode: "page", cover: sentence.trim(), body: markdown.slice(start + sentence.length).trim() };
    }
  }
  return { mode: "page", cover: FALLBACK_COVER, body: markdown };
}
