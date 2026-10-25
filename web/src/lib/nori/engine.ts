"use client";

import { classify, extractHints, extractSvg, svgSize } from "../hybrid";
import { MAIN_THREAD } from "../seed";
import { getNori, artifactTypeLabel } from "../store";
import { clock, nextRun, relative, upcomingAt } from "../time";
import { firstSentence, uid } from "../utils";
import type { Chip, GoalCategory, ID, ImageItem, TextItem, ThreadItem, VoiceItem } from "../types";
import { NoriRefused, routeBackend, type ApiContent, type ApiMessage, type NoriBackend } from "./backend";

const backend: NoriBackend = routeBackend;
const live = new Map<ID, AbortController>();

/** Bumped by Start fresh; a reply that wakes up into a newer generation stops. */
let generation = 0;
class Cancelled extends Error {}

const wait = async (ms: number) => {
  const g = generation;
  await new Promise((r) => setTimeout(r, ms));
  if (g !== generation) throw new Cancelled();
};

export function cancelAll() {
  generation += 1;
  live.forEach((c) => c.abort());
  live.clear();
}

/* ── Small helpers used by scripts ─────────────────────────── */

/** Clears the indicator only if nothing newer took it over meanwhile. */
function clearBusy(threadId: ID, token: object) {
  const s = getNori();
  const patch: Partial<ReturnType<typeof getNori>> = {};
  if (s.typing === threadId && busyToken.get(threadId) === token) patch.typing = null;
  if (s.work?.threadId === threadId && busyToken.get(threadId) === token) patch.work = null;
  if (busyToken.get(threadId) === token) {
    busyToken.delete(threadId);
    if (s.markState === "thinking" || s.markState === "writing") patch.markState = "idle";
  }
  if (Object.keys(patch).length) s.set(patch);
}
const busyToken = new Map<ID, object>();
const claimBusy = (threadId: ID) => {
  const token = {};
  busyToken.set(threadId, token);
  return token;
};

async function typing(threadId: ID, ms: number) {
  const token = claimBusy(threadId);
  getNori().set({ typing: threadId, markState: "thinking" });
  try {
    await wait(ms);
  } finally {
    clearBusy(threadId, token);
  }
}

function say(threadId: ID, text: string, extra: Partial<TextItem> = {}, animate = true) {
  const item: TextItem = { id: uid("m"), kind: "text", sender: "nori", at: Date.now(), text, ...extra };
  getNori().appendItem(threadId, item, { animate });
  if (item.activityId) getNori().linkActivity(item.activityId, threadId, item.id);
  return item.id;
}

function pleased() {
  getNori().set({ markState: "pleased" });
  setTimeout(() => getNori().set({ markState: "idle" }), 900);
}

function chips(...labels: (string | [string, string])[]): TextItem["chips"] {
  return {
    options: labels.map((l) => (Array.isArray(l) ? { id: uid("c"), label: l[0], script: l[1] } : { id: uid("c"), label: l })),
  };
}

/** Swallows the cancellation that Reset raises inside a sleeping moment. */
function guarded<A extends unknown[]>(fn: (...args: A) => Promise<void>) {
  return async (...args: A) => {
    try {
      await fn(...args);
    } catch (err) {
      if (!(err instanceof Cancelled)) throw err;
    }
  };
}

/* ── Live replies ──────────────────────────────────────────── */

function itemToText(item: ThreadItem, artifacts: ReturnType<typeof getNori>["artifacts"], approvals: ReturnType<typeof getNori>["approvals"]): string {
  switch (item.kind) {
    case "text": {
      const art = item.artifactId ? artifacts.find((a) => a.id === item.artifactId) : undefined;
      const chosen = item.chips?.options.find((c) => c.id === item.chips?.chosenId);
      const offered = item.chips && !chosen ? `\n[Offered replies: ${item.chips.options.map((c) => c.label).join(" | ")}]` : "";
      return `${item.text}${art ? `\n[Saved to Library: ${art.title}]` : ""}${offered}`;
    }
    case "voice":
      return `[Voice note, ${Math.round(item.durationMs / 1000)}s] ${item.transcript ?? "(no transcript)"}`;
    case "image":
      return `[Image: ${item.alt}]`;
    case "attachment": {
      const art = artifacts.find((a) => a.id === item.artifactId);
      return `[Attached from Library: ${art?.title ?? "a file"}${art?.body ? `]\n${art.body}` : "]"}`;
    }
    case "approval": {
      const ap = approvals.find((a) => a.id === item.approvalId);
      return ap ? `[Approval card: ${ap.title}. ${ap.what} Status: ${ap.status}]` : "";
    }
  }
}

function toApiMessages(threadId: ID, nudge?: string): ApiMessage[] {
  const s = getNori();
  const thread = s.threads.find((t) => t.id === threadId);
  if (!thread) return [];
  const items = thread.items.slice(-40);
  // Only photos from your latest turn go up as pixels; older ones are described.
  const lastNori = items.map((i) => i.sender).lastIndexOf("nori");
  const out: ApiMessage[] = [];
  for (const [index, item] of items.entries()) {
    const role = item.sender === "me" ? "user" : "assistant";
    let blocks: ApiContent[] = [];
    const recent = index > lastNori;
    const media = item.kind === "image" && item.sender === "me" && recent ? /^data:(image\/(?:jpeg|png|gif|webp));base64,/.exec(item.src) : null;
    if (media && item.kind === "image") {
      const type = media[1] as "image/jpeg" | "image/png" | "image/gif" | "image/webp";
      blocks = [{ type: "image", source: { type: "base64", media_type: type, data: item.src.slice(media[0].length) } }];
    } else {
      const text = itemToText(item, s.artifacts, s.approvals);
      if (text) blocks = [{ type: "text", text }];
    }
    if (item.replyToId) {
      const quoted = thread.items.find((i) => i.id === item.replyToId);
      if (quoted) blocks.unshift({ type: "text", text: `[Replying to: “${firstSentence(itemToText(quoted, s.artifacts, s.approvals))}”]` });
    }
    const reacted = item.reactions?.find((r) => r.by === "nori");
    if (reacted && item.sender === "me") blocks.push({ type: "text", text: `[${s.identity.name} reacted ${reacted.emoji}]` });
    if (!blocks.length) continue;
    const last = out[out.length - 1];
    if (last && last.role === role && Array.isArray(last.content)) last.content.push(...blocks);
    else out.push({ role, content: blocks });
  }
  if (nudge) {
    const last = out[out.length - 1];
    const block: ApiContent = { type: "text", text: nudge };
    if (last?.role === "user" && Array.isArray(last.content)) last.content.push(block);
    else out.push({ role: "user", content: [block] });
  }
  if (out[0]?.role === "assistant") out.unshift({ role: "user", content: [{ type: "text", text: "[Earlier in our chat]" }] });
  return out;
}

export function buildContext() {
  const s = getNori();
  const now = Date.now();
  const lines: string[] = [];
  lines.push(`Right now it's ${new Date(now).toLocaleString("en-US", { weekday: "long", month: "long", day: "numeric", hour: "numeric", minute: "2-digit" })}.`);
  lines.push(`Your name is ${s.identity.name}.`);
  lines.push(`\n# Soul (who you are; follow it)\n${s.identity.soul.content}`);
  lines.push(`\n# Memory (what you know about the user)\n${s.identity.memory.content}`);
  lines.push(`\n# Recent activity (newest first)`);
  s.activity.slice(0, 12).forEach((a) => lines.push(`- ${relative(a.at, now)} · ${a.title}: ${a.summary}`));
  const pending = s.approvals.filter((a) => a.status === "pending");
  lines.push(`\n# Waiting on the user's approval`);
  lines.push(pending.length ? pending.map((a) => `- ${a.title}: ${a.what}`).join("\n") : "- Nothing.");
  lines.push(`\n# Scheduled`);
  s.upcoming.forEach((u) => {
    const at = upcomingAt(u, now);
    if (at) lines.push(`- ${u.title}: ${u.schedule}, next ${nextRun(at, now)}${u.paused ? " (paused)" : ""}`);
  });
  lines.push(`\n# Goals`);
  s.goals.forEach((g) => lines.push(`- ${g.title} (${g.status}): ${g.line}`));
  lines.push(`\n# Library`);
  s.artifacts.slice(0, 12).forEach((a) => lines.push(`- ${a.title} (${artifactTypeLabel[a.type]})`));
  return lines.join("\n");
}

/** Nori's reaction on your latest message; one at a time, so a new one replaces it. */
function react(threadId: ID, emoji: string) {
  const s = getNori();
  const mine = [...(s.threads.find((t) => t.id === threadId)?.items ?? [])].reverse().find((i) => i.sender === "me");
  if (!mine) return;
  s.updateItem(threadId, mine.id, { reactions: [...(mine.reactions ?? []).filter((r) => r.by !== "nori"), { emoji, by: "nori" }] });
}

/** `streamed`: the words were already on screen, so the bubbles settle in place instead of arriving. */
function finalize(threadId: ID, raw: string, streamed = false) {
  const s = getNori();
  const { body, hints } = extractHints(raw);
  if (hints.react) react(threadId, hints.react);
  if (!body) return;
  const replyChips = hints.chips?.length ? { options: hints.chips.map((label) => ({ id: uid("c"), label })) } : undefined;
  if (hints.artifact) {
    const split = classify(body, "page");
    const artifactId = s.addArtifact({
      title: hints.artifact.title,
      type: hints.artifact.type,
      madeBy: "nori",
      body: split.body,
      thumb: hints.artifact.type === "dashboard" ? "dash" : /\|.*\|/.test(split.body) ? "table" : "doc",
    });
    const activityId = s.addActivity({
      type: "done",
      title: `Made “${hints.artifact.title}”`,
      summary: `${artifactTypeLabel[hints.artifact.type]} saved to Library.`,
      threadId,
    });
    say(threadId, split.cover!, { artifactId, chips: replyChips, activityId });
    pleased();
    return;
  }
  const mode = (t: string) => (hints.mode ? `${t}\n<!-- nori:${hints.mode} -->` : t);
  const drawing = hints.image ? extractSvg(body) : null;
  const before = drawing ? drawing.before : body;
  const after = drawing?.after ?? "";
  // An approval card carries its own buttons, so chips would compete with it.
  const lastChips = hints.approval ? undefined : replyChips;
  if (before) say(threadId, mode(before), after ? {} : { chips: lastChips }, !streamed);
  if (drawing) {
    const { w, h } = svgSize(drawing.svg);
    const item: ImageItem = {
      id: uid("img"),
      kind: "image",
      sender: "nori",
      at: Date.now(),
      src: `data:image/svg+xml;charset=utf-8,${encodeURIComponent(drawing.svg)}`,
      alt: hints.image!.alt,
      w,
      h,
    };
    s.appendItem(threadId, item);
  }
  if (after) say(threadId, mode(after), { chips: lastChips }, !streamed);
  if (hints.approval) {
    const messageId = uid("ap-m");
    const approvalId = s.addApproval({ ...hints.approval, threadId, messageId });
    s.appendItem(threadId, { id: messageId, kind: "approval", sender: "nori", at: Date.now(), approvalId });
    const activityId = s.addActivity({ type: "drafted", title: hints.approval.title, summary: "Waiting on your OK.", threadId });
    s.linkActivity(activityId, threadId, messageId);
  }
}

/**
 * Paces a live reply onto the screen a word at a time. The network arrives in
 * bursts; this smooths it to a steady read that speeds up when it falls behind,
 * so it never lags the model by more than about half a second.
 */
function revealer(threadId: ID) {
  let target = "";
  let shown = 0;
  let last = 0;
  let frame = 0;
  let settle: (() => void) | null = null;
  const reduced = typeof window !== "undefined" && window.matchMedia("(prefers-reduced-motion: reduce)").matches;

  const tick = (now: number) => {
    const dt = last ? Math.min(64, now - last) : 16;
    last = now;
    const backlog = target.length - shown;
    const rate = Math.max(70, backlog * 2.4); // characters per second
    let next = reduced ? target.length : Math.min(target.length, shown + Math.max(1, Math.round((rate * dt) / 1000)));
    // Whole words only: finish the word we're in.
    if (next < target.length) {
      const space = target.slice(next).search(/\s/);
      next = space < 0 ? target.length : next + space;
    }
    if (next !== shown) {
      shown = next;
      getNori().set({ stream: { threadId, text: target.slice(0, shown) } });
    }
    if (shown < target.length) frame = requestAnimationFrame(tick);
    else {
      frame = 0;
      last = 0;
      settle?.();
      settle = null;
    }
  };

  return {
    push(text: string) {
      target = text;
      if (!frame) frame = requestAnimationFrame(tick);
    },
    /** Resolves once everything pushed so far is on screen. */
    flush() {
      return shown >= target.length ? Promise.resolve() : new Promise<void>((r) => (settle = r));
    },
    stop() {
      cancelAnimationFrame(frame);
      frame = 0;
      settle?.();
      settle = null;
      if (getNori().stream?.threadId === threadId) getNori().set({ stream: null });
    },
  };
}

/** `nudge` is a note in your voice for turns you didn't type, such as tapping Approve. */
export async function liveReply(threadId: ID, nudge?: string) {
  live.get(threadId)?.abort();
  const controller = new AbortController();
  live.set(threadId, controller);
  const token = claimBusy(threadId);
  const s = getNori();
  s.set({ typing: threadId, work: null, markState: "thinking", liveError: null, stream: null });
  const reveal = revealer(threadId);
  let text = "";
  let started = false;
  try {
    for await (const chunk of backend.reply({ messages: toApiMessages(threadId, nudge), context: buildContext() }, controller.signal)) {
      text += chunk;
      if (!started && extractHints(text).body) {
        started = true;
        getNori().set({ typing: null, markState: "writing" });
      }
      reveal.push(text);
    }
    if (controller.signal.aborted) return;
    await reveal.flush();
    if (controller.signal.aborted) return;
    clearBusy(threadId, token);
    // Same task: the draft leaves and the real items take its place in one paint.
    finalize(threadId, text, started);
    getNori().set({ stream: null });
    if (started) pleased();
  } catch (err) {
    if (controller.signal.aborted) return;
    getNori().set({
      liveError: {
        threadId,
        message: err instanceof NoriRefused ? "Nori can't help with that one." : err instanceof Error ? err.message : "Couldn't reach Nori.",
      },
    });
  } finally {
    reveal.stop();
    if (live.get(threadId) === controller) live.delete(threadId);
    // A newer reply on this thread owns the indicator now; leave it alone.
    clearBusy(threadId, token);
  }
}

/* ── Messaging first (P5): reason up front, a way out, quiet hours, daily cap ── */

const toMinutes = (hhmm: string) => {
  const [h, m] = hhmm.split(":").map(Number);
  return h * 60 + m;
};

export function inQuietHours(now = new Date()) {
  const { quietStart, quietEnd } = getNori().settings;
  const t = now.getHours() * 60 + now.getMinutes();
  const start = toMinutes(quietStart);
  const end = toMinutes(quietEnd);
  return start > end ? t >= start || t < end : t >= start && t < end;
}

export function canMessageFirst() {
  const s = getNori();
  if (!s.settings.proactive || inQuietHours()) return false;
  const today = new Date().toDateString();
  const sent = s.threads.flatMap((t) => t.items).filter((i) => i.kind === "text" && i.proactive && new Date(i.at).toDateString() === today).length;
  return sent < s.settings.dailyCap;
}

/* ── What the UI calls ─────────────────────────────────────── */

export function retryLive(threadId: ID) {
  getNori().set({ liveError: null });
  void liveReply(threadId);
}

export async function sendText(threadId: ID, text: string, opts: { replyToId?: ID } = {}) {
  const s = getNori();
  s.appendItem(threadId, { id: uid("m"), kind: "text", sender: "me", at: Date.now(), text, replyToId: opts.replyToId });
  renameSideChat(threadId, text);
  await liveReply(threadId);
}

export async function sendItems(threadId: ID, items: ThreadItem[]) {
  const s = getNori();
  items.forEach((i) => s.appendItem(threadId, i));
  await liveReply(threadId);
}

export async function sendVoice(threadId: ID, voice: Omit<VoiceItem, "id" | "kind" | "sender" | "at">) {
  getNori().appendItem(threadId, { id: uid("v"), kind: "voice", sender: "me", at: Date.now(), ...voice });
  await liveReply(threadId);
}

export function imageItem(src: string, alt: string, w: number, h: number): ImageItem {
  return { id: uid("img"), kind: "image", sender: "me", at: Date.now(), src, alt, w, h };
}

export async function chooseChip(threadId: ID, itemId: ID, chip: Chip) {
  const s = getNori();
  s.chooseChip(threadId, itemId, chip.id);
  s.appendItem(threadId, { id: uid("m"), kind: "text", sender: "me", at: Date.now(), text: chip.label, fromChip: chip.id });
  await liveReply(threadId);
}

export async function resolveApproval(approvalId: ID, approve: boolean) {
  const s = getNori();
  const ap = s.approvals.find((a) => a.id === approvalId);
  if (!ap || ap.status !== "pending") return;
  // Nothing runs yet, so the card records the decision and Nori answers it.
  s.resolveApproval(approvalId, approve, approve ? "Approved" : "Not now");
  await liveReply(ap.threadId, approve ? `[I tapped Approve on “${ap.title}”.]` : `[I tapped Not now on “${ap.title}”.]`);
}

function goChat(threadId: ID = MAIN_THREAD) {
  getNori().set({ activeThreadId: threadId, drawerOpen: false, searchOpen: false });
}

/** Ideas → Start in chat: Nori posts the idea with chips. */
async function startIdeaImpl(ideaId: ID) {
  const idea = getNori().ideas.find((i) => i.id === ideaId);
  if (!idea) return;
  goChat();
  await typing(MAIN_THREAD, 650);
  say(MAIN_THREAD, idea.pitch, { chips: { options: idea.chips.map((c) => ({ ...c, id: uid("c") })) } });
}

async function talkAboutGoalImpl(goalId: ID) {
  const goal = getNori().goals.find((g) => g.id === goalId);
  if (!goal) return;
  goChat();
  getNori().set({ goalSheetId: null });
  await typing(MAIN_THREAD, 650);
  say(MAIN_THREAD, goal.talkPitch, { chips: { options: goal.talkChips.map((c) => ({ ...c, id: uid("c") })) } });
}

const CATEGORY_STARTS: Record<GoalCategory, { pitch: string; options: string[] }> = {
  health: { pitch: "Let's set up a health goal. What would you like to work toward?", options: ["Sleep by 11 on weeknights", "Walk 8,000 steps a day"] },
  relationships: { pitch: "Let's set up a relationships goal. Who's it about?", options: ["Call my parents every Sunday", "Plan one night out a month"] },
  finance: { pitch: "Let's set up a money goal. What are you aiming for?", options: ["Spend less on takeout", "Build a $5,000 cushion"] },
  career: { pitch: "Let's set up a career goal. What's on your mind?", options: ["Ship one side project", "Ask for feedback monthly"] },
  interests: { pitch: "Let's set up a goal for something you enjoy. What is it?", options: ["Learn to solder properly", "Finish one print a week"] },
  productivity: { pitch: "Let's set up a productivity goal. What would help most?", options: ["Inbox zero on Fridays", "Plan tomorrow every evening"] },
  other: { pitch: "Let's set up a goal. Tell me what you have in mind.", options: [] },
};

async function startGoalCategoryImpl(category: GoalCategory) {
  goChat();
  const start = CATEGORY_STARTS[category];
  await typing(MAIN_THREAD, 600);
  say(MAIN_THREAD, start.pitch, start.options.length ? { chips: chips(...start.options, "Something else") } : {});
}

function renameSideChat(threadId: ID, text: string) {
  const t = getNori().threads.find((x) => x.id === threadId);
  if (t?.kind !== "side" || t.title !== "New side chat") return;
  const title = text.replace(/\s+/g, " ").trim().split(" ").slice(0, 5).join(" ");
  getNori().renameThread(threadId, title.length > 32 ? `${title.slice(0, 31)}…` : title);
}

export const timeLabel = clock;

export const startIdea = guarded(startIdeaImpl);

export const talkAboutGoal = guarded(talkAboutGoalImpl);

export const startGoalCategory = guarded(startGoalCategoryImpl);
