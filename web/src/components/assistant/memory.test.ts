import assert from "node:assert/strict";
import { describe, test } from "node:test";
import type { MemoryFact, SearchHit, WorkingMemory } from "@/lib/contracts/assistant";
import { jumpHref } from "./LibraryScreen";
import {
  buildMemoryHref,
  coreBudgetStats,
  DEFAULT_CORE_BUDGET_BYTES,
  filterCoreMemories,
  formatByteBudget,
  formatKB,
  formatWatermark,
  groupMemoriesByTopic,
  MAX_MEMORY_TEXT_BYTES,
  memoryHitHref,
  messageJumpHref,
  normalizeMemoryTopic,
  parseMemoryTab,
  provenanceInfo,
  requiresReauthorForPromote,
  utf8Bytes,
  validateMemoryText,
  validatePromoteDraft,
  workingMemoryWatermarks,
} from "./MemoryScreen";

describe("memory jump URL helpers", () => {
  test("LibraryScreen jumpHref routes core and log memory hits with deep-link params", () => {
    const coreHit: SearchHit = {
      kind: "memory",
      id: "core:mem_2",
      title: "Preferences",
      snippet: "Always use UTC timestamps.",
      jump: { kind: "memory", layer: "core", memory_id: "mem_2" },
    };
    assert.equal(jumpHref(coreHit), "/memory?tab=core&id=mem_2");

    const logHit: SearchHit = {
      kind: "memory",
      id: "log:2026-11-23:entry_1",
      title: "Decisions",
      snippet: "Chose hash-partitioned FTS.",
      jump: { kind: "memory", layer: "log", log_date: "2026-11-23", entry_id: "entry_1" },
    };
    assert.equal(jumpHref(logHit), "/memory?tab=logs&date=2026-11-23&entry=entry_1");

    const logDateOnlyHit: SearchHit = {
      kind: "memory",
      id: "log:2026-11-23",
      title: "Daily log",
      snippet: "Notes",
      jump: { kind: "memory", layer: "log", log_date: "2026-11-23" },
    };
    assert.equal(jumpHref(logDateOnlyHit), "/memory?tab=logs&date=2026-11-23");

    const plainMemoryHit: SearchHit = {
      kind: "memory",
      id: "mem_fallback",
      title: "Memory",
      snippet: "Fallback",
      jump: { kind: "memory" },
    };
    assert.equal(jumpHref(plainMemoryHit), "/memory");

    const noJumpMemoryHit: SearchHit = {
      kind: "memory",
      id: "mem_nojump",
      title: "Memory",
      snippet: "Fallback",
    };
    assert.equal(jumpHref(noJumpMemoryHit), "/memory");
  });

  test("memoryHitHref, buildMemoryHref, parseMemoryTab, and messageJumpHref handle all deep-link combinations", () => {
    assert.equal(memoryHitHref({ layer: "core", memory_id: "mem_9" }), "/memory?tab=core&id=mem_9");
    assert.equal(
      memoryHitHref({ layer: "log", log_date: "2026-11-23", entry_id: "e_42" }),
      "/memory?tab=logs&date=2026-11-23&entry=e_42",
    );
    assert.equal(buildMemoryHref({ tab: "working" }), "/memory?tab=working");
    assert.equal(buildMemoryHref({ tab: "core" }), "/memory");
    assert.equal(buildMemoryHref({ tab: "logs", q: "postgres" }), "/memory?tab=logs&q=postgres");

    assert.equal(parseMemoryTab("core"), "core");
    assert.equal(parseMemoryTab("logs"), "logs");
    assert.equal(parseMemoryTab("working"), "working");
    assert.equal(parseMemoryTab(null, "2026-11-23", null), "logs");
    assert.equal(parseMemoryTab(null, null, "entry_1"), "logs");
    assert.equal(parseMemoryTab("invalid"), "core");

    assert.equal(messageJumpHref("msg_12", "message"), "/#msg_12");
    assert.equal(messageJumpHref("in_7", "input"), "/#in_7-user");
    assert.equal(messageJumpHref("in_7-user", "input"), "/#in_7-user");
    assert.equal(messageJumpHref("ui", "input"), undefined);
    assert.equal(messageJumpHref("", "message"), undefined);
  });
});

describe("memory byte budget formatting and calculations", () => {
  test("formatKB and formatByteBudget format zero, sub-0.1 KB, fractional, and integer KB cleanly", () => {
    assert.equal(formatKB(0), "0");
    assert.equal(formatKB(-100), "0");
    assert.equal(formatKB(50), "<0.1");
    assert.equal(formatKB(1420), "1.4");
    assert.equal(formatKB(2048), "2");
    assert.equal(formatKB(15360), "15");
    assert.equal(formatKB(24576), "24");

    assert.equal(formatByteBudget(0), "0 / 24 KB");
    assert.equal(formatByteBudget(1420, 24576), "1.4 / 24 KB");
    assert.equal(formatByteBudget(24576, 24576), "24 / 24 KB");
    assert.equal(formatByteBudget(1024, 0), "1 / 24 KB");
  });

  test("coreBudgetStats prefers working.core_bytes and falls back to UTF-8 sum of facts", () => {
    const facts = [{ text: "Prefers aisle seats" }, { text: "日本語テスト" }];
    assert.equal(utf8Bytes(facts[0].text), 19);
    assert.equal(utf8Bytes(facts[1].text), 18);

    const fallback = coreBudgetStats(facts);
    assert.equal(fallback.used, 37);
    assert.equal(fallback.budget, DEFAULT_CORE_BUDGET_BYTES);
    assert.equal(fallback.tone, "neutral");

    const fromWorking = coreBudgetStats(facts, { core_bytes: 21504, core_budget_bytes: 24576 });
    assert.equal(fromWorking.used, 21504);
    assert.equal(fromWorking.tone, "warning");

    const nearFull = coreBudgetStats(facts, { core_bytes: 24200, core_budget_bytes: 24576 });
    assert.equal(nearFull.tone, "danger");
  });
});

describe("memory text validation and task-outcome promote guard", () => {
  test("validateMemoryText enforces non-empty and 2000 UTF-8 byte limit", () => {
    assert.equal(validateMemoryText("   ").ok, false);

    const exact = "a".repeat(MAX_MEMORY_TEXT_BYTES);
    const ok = validateMemoryText(exact);
    assert.equal(ok.ok, true);
    assert.equal(ok.bytes, MAX_MEMORY_TEXT_BYTES);

    const tooLongAscii = "a".repeat(MAX_MEMORY_TEXT_BYTES + 1);
    assert.equal(validateMemoryText(tooLongAscii).ok, false);

    const exactEmoji = "🚀".repeat(500);
    assert.equal(validateMemoryText(exactEmoji).ok, true);
    const overEmoji = "🚀".repeat(501);
    assert.equal(validateMemoryText(overEmoji).ok, false);
  });

  test("task_outcome entries require explicit user review before promoting to Core Memory", () => {
    assert.equal(requiresReauthorForPromote("task_outcome"), true);
    assert.equal(requiresReauthorForPromote("pre_compact_flush"), false);
    assert.equal(requiresReauthorForPromote("summary_evicted"), false);
    assert.equal(requiresReauthorForPromote("dream_curated"), false);
    assert.equal(requiresReauthorForPromote("user_explicit"), false);

    const taskEntry = { provenance: "task_outcome" as const, text: "Drafted Q4 budget spreadsheet." };

    const blocked = validatePromoteDraft(taskEntry, taskEntry.text, false);
    assert.equal(blocked.ok, false);

    const approved = validatePromoteDraft(taskEntry, "Q4 budget is tracked in the shared spreadsheet.", true);
    assert.equal(approved.ok, true);
    if (approved.ok) assert.equal(approved.text, "Q4 budget is tracked in the shared spreadsheet.");

    const approvedOverLimit = validatePromoteDraft(taskEntry, "x".repeat(MAX_MEMORY_TEXT_BYTES + 1), true);
    assert.equal(approvedOverLimit.ok, false);

    const userEntry = { provenance: "pre_compact_flush" as const, text: "Always use UTC timestamps." };
    const direct = validatePromoteDraft(userEntry, userEntry.text, false);
    assert.equal(direct.ok, true);
  });

  test("filterCoreMemories matches text, section, about, provenance, and source_text", () => {
    const facts: MemoryFact[] = [
      {
        id: "m1",
        revision: 1,
        text: "Always use UTC ISO-8601 timestamps.",
        section: "Preferences",
        about: "reporting",
        provenance: "pre_compact_flush",
        scope: "user",
        origin: {},
        created_at: "2026-11-23T09:00:00Z",
      },
      {
        id: "m2",
        revision: 2,
        text: "Lives in London.",
        section: "Personal",
        provenance: "dream_curated",
        source_text: "I moved to Hackney last year.",
        scope: "user",
        origin: {},
        created_at: "2026-11-23T10:00:00Z",
      },
    ];
    assert.equal(filterCoreMemories(facts, "").length, 2);
    assert.deepEqual(filterCoreMemories(facts, "UTC").map((f) => f.id), ["m1"]);
    assert.deepEqual(filterCoreMemories(facts, "reporting").map((f) => f.id), ["m1"]);
    assert.deepEqual(filterCoreMemories(facts, "hackney").map((f) => f.id), ["m2"]);
    assert.deepEqual(filterCoreMemories(facts, "curated").map((f) => f.id), ["m2"]);
    assert.equal(provenanceInfo("task_outcome").tone, "warning");
    assert.equal(provenanceInfo(undefined).label, "Explicit");
  });
});

describe("working memory watermark formatting", () => {
  test("workingMemoryWatermarks handles empty, session-idle extracted, and compacted states", () => {
    const now = Date.parse("2026-11-23T14:00:00Z");

    const empty = workingMemoryWatermarks({}, now, "UTC");
    assert.equal(empty.extractedThrough, "Not extracted yet");
    assert.equal(empty.flushedThrough, "Not flushed yet");
    assert.equal(empty.windowThrough, "—");
    assert.equal(empty.lastDream, "Not yet");

    const idleExtracted: Pick<WorkingMemory, "extracted_through" | "flushed_through" | "through" | "last_dream_at"> = {
      extracted_through: "2026-11-23T13:30:00Z",
    };
    const idle = workingMemoryWatermarks(idleExtracted, now, "UTC");
    assert.match(idle.extractedThrough, /13:30.*30 minutes ago/);
    assert.equal(idle.flushedThrough, "Not flushed yet");
    assert.equal(idle.windowThrough, "—");

    const compacted = workingMemoryWatermarks(
      {
        extracted_through: "2026-11-23T13:40:00Z",
        flushed_through: "2026-11-23T12:00:00Z",
        through: "2026-11-23T12:00:00Z",
        last_dream_at: "2026-11-23T11:00:00Z",
      },
      now,
      "UTC",
    );
    assert.match(compacted.extractedThrough, /13:40.*20 minutes ago/);
    assert.match(compacted.flushedThrough, /12:00.*2 hours ago/);
    assert.match(compacted.windowThrough, /12:00.*2 hours ago/);
    assert.match(compacted.lastDream, /11:00.*3 hours ago/);
    assert.equal(formatWatermark(undefined, "Not extracted yet", now, "UTC"), "Not extracted yet");
  });

  test("normalizeMemoryTopic and groupMemoriesByTopic organize facts into canonical topic folders", () => {
    assert.equal(normalizeMemoryTopic(undefined), "About you");
    assert.equal(normalizeMemoryTopic("profile"), "About you");
    assert.equal(normalizeMemoryTopic("Preferences"), "Preferences");
    assert.equal(normalizeMemoryTopic("work"), "Projects");
    assert.equal(normalizeMemoryTopic("family"), "People");
    assert.equal(normalizeMemoryTopic("routine"), "Home & Routine");
    assert.equal(normalizeMemoryTopic("dining"), "Travel & Food");
    assert.equal(normalizeMemoryTopic("guitar"), "Guitar");

    const make = (id: string, section?: string): MemoryFact => ({
      id,
      revision: 1,
      text: `Fact ${id}`,
      section,
      scope: "user",
      created_at: "2026-11-27T00:00:00Z",
      origin: {},
    });

    const grouped = groupMemoriesByTopic([
      make("1", "guitar"),
      make("2", "dining"),
      make("3", "Preferences"),
      make("4", undefined),
      make("5", "profile"),
    ]);
    assert.deepEqual(
      grouped.map((g) => [g.topic, g.items.length]),
      [
        ["About you", 2],
        ["Preferences", 1],
        ["Travel & Food", 1],
        ["Guitar", 1],
      ],
    );
  });
});
