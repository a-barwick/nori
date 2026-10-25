import { fromMarkdown } from "mdast-util-from-markdown";
import { gfmFromMarkdown } from "mdast-util-gfm";
import { gfm } from "micromark-extension-gfm";
import type { List, Nodes, Paragraph, Root, RootContent } from "mdast";
import type { ArtifactType } from "./types";

/**
 * The hybrid rule (spec §5.2). Bubble vs page is decided from the markdown's
 * structure, never by model whim. The model can force a mode with a hint:
 *   <!-- nori:page -->  <!-- nori:bubble -->
 * and attach extras:
 *   <!-- nori:chips Yes, do it | Not now -->
 *   <!-- nori:artifact type="document" title="Parts list" -->
 *   <!-- nori:approval title="Send a message to the seller" what="…" why="…" impact="…" -->
 *   <!-- nori:image alt="Desk display mockup" -->  (pairs with the reply's ```svg block)
 *   <!-- nori:react ❤️ -->  (on your latest message; may be the whole reply)
 */

export const PAGE_WORDS = 150;
export const MAX_BUBBLE_LIST_ITEMS = 5;
export const MAX_BUBBLE_CODE_LINES = 3;

export interface Hints {
  mode?: "page" | "bubble";
  chips?: string[];
  artifact?: { type: ArtifactType; title: string };
  approval?: { title: string; what: string; why: string; impact: string };
  image?: { alt: string };
  react?: string;
}

const attr = (arg: string, name: string) => new RegExp(`${name}="([^"]*)"`).exec(arg)?.[1]?.trim();

const HINT = /<!--\s*nori:([a-z]+)([\s\S]*?)-->/g;
const ARTIFACT_TYPES: ArtifactType[] = ["document", "page", "dashboard", "image", "audio", "file"];

export function extractHints(markdown: string): { body: string; hints: Hints } {
  const hints: Hints = {};
  let body = markdown.replace(HINT, (_m, name: string, rest: string) => {
    const arg = rest.trim();
    if (name === "page" || name === "bubble") hints.mode = name;
    if (name === "chips") {
      hints.chips = arg
        .split("|")
        .map((s) => s.trim())
        .filter(Boolean)
        .slice(0, 4);
    }
    if (name === "artifact") {
      const type = attr(arg, "type") as ArtifactType | undefined;
      const title = attr(arg, "title");
      if (title) hints.artifact = { type: type && ARTIFACT_TYPES.includes(type) ? type : "document", title };
    }
    if (name === "approval") {
      const title = attr(arg, "title");
      const what = attr(arg, "what");
      if (title && what) hints.approval = { title, what, why: attr(arg, "why") ?? "", impact: attr(arg, "impact") ?? "" };
    }
    if (name === "react" && arg && arg.length <= 8) hints.react = arg;
    if (name === "image") hints.image = { alt: attr(arg, "alt") || "Image" };
    return "";
  });
  // While streaming, hide a hint that hasn't closed yet.
  body = body.replace(/<!--(?![\s\S]*-->)[\s\S]*$/, "");
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

export function wordCount(markdown: string) {
  const text = textOf(parse(markdown));
  return text.split(/\s+/).filter((w) => /\w/.test(w)).length;
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

export interface Classified {
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

/** Rough check used while a live reply streams in, to switch typing → "writing it up". */
export function looksLikePage(partial: string) {
  if (/^#{1,6}\s|\n#{1,6}\s|\n\|.*\|\s*\n|```[\s\S]*\n[\s\S]*\n[\s\S]*\n/.test(partial)) return true;
  return partial.split(/\s+/).length > PAGE_WORDS;
}

const SVG_FENCE = /```svg[^\n]*\n([\s\S]*?)```/;

/** Pulls the reply's ```svg block out as an image; the text around it stays. */
export function extractSvg(body: string): { before: string; svg: string; after: string } | null {
  const m = SVG_FENCE.exec(body);
  if (!m || !/^\s*<svg[\s>]/.test(m[1])) return null;
  return { before: body.slice(0, m.index).trim(), svg: m[1].trim(), after: body.slice(m.index + m[0].length).trim() };
}

/** Intrinsic size from width/height or the viewBox; 4:3 when neither is usable. */
export function svgSize(svg: string): { w: number; h: number } {
  const tag = /<svg[^>]*>/.exec(svg)?.[0] ?? "";
  const num = (n: string) => Number(/^[\d.]+/.exec(n)?.[0]);
  const w = num(/\swidth="([^"]+)"/.exec(tag)?.[1] ?? "");
  const h = num(/\sheight="([^"]+)"/.exec(tag)?.[1] ?? "");
  if (w > 0 && h > 0) return { w, h };
  const vb = /viewBox="([^"]+)"/.exec(tag)?.[1]?.split(/[\s,]+/).map(Number);
  if (vb?.length === 4 && vb[2] > 0 && vb[3] > 0) return { w: vb[2], h: vb[3] };
  return { w: 400, h: 300 };
}
