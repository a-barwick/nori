import assert from "node:assert/strict";
import { describe, test } from "node:test";
import React from "react";
import { renderToStaticMarkup } from "react-dom/server";
import { Thread } from "@/components/chat/Thread";
import { NoriTabIcon } from "@/components/mark/NoriMark";
import {
  APPEARANCE_BOOT_SCRIPT,
  APPEARANCE_STORAGE_KEY,
  appearanceLabel,
  parseAppearance,
} from "@/lib/appearance";
import { AssistantProvider } from "@/lib/assistant/context";
import type { Automation, WorkItem } from "@/lib/contracts/assistant";
import { buildUnifiedTaskEntries, parseWorkTab, statusPill, WORK_HUB_SECTION_LIMIT } from "./WorkScreen";
import { THINKING_WORDS } from "@/lib/thinkingWords";
import { buildUnits } from "@/lib/units";
import { isChatInlineInteraction } from "@/lib/assistant/interactions";

describe("Web UI simplification pass & design system parity", () => {
  test("appearance helpers normalize values and emit anti-FOUC boot script", () => {
    assert.equal(APPEARANCE_STORAGE_KEY, "nori.appearance");
    assert.equal(parseAppearance("dark"), "dark");
    assert.equal(parseAppearance("light"), "light");
    assert.equal(parseAppearance("system"), "system");
    assert.equal(parseAppearance("invalid"), "system");
    assert.equal(parseAppearance(null), "system");
    assert.equal(appearanceLabel("system"), "System");
    assert.equal(appearanceLabel("light"), "Light");
    assert.equal(appearanceLabel("dark"), "Dark");
    assert.match(APPEARANCE_BOOT_SCRIPT, /nori\.appearance/);
    assert.match(APPEARANCE_BOOT_SCRIPT, /dataset\.appearance/);
  });

  test("Work statusPill aligns terminology and semantic tones with iOS", () => {
    const base: WorkItem = {
      task_id: "t1",
      title: "Test task",
      status: "running",
      bucket: "needs_you",
      started_at: "2026-11-25T10:00:00Z",
      origin: { kind: "conversation" },
      operations: { total: 0, submitted: 0, uncertain: 0, failed: 0 },
      cost: { status: "known", amount_usd: 0 },
      result_ids: [],
      instruction_revision: 1,
      interaction_ids: [],
      cancel_requested: false,
    };
    assert.deepEqual(statusPill(base), { tone: "warning", label: "Action needed" });
    assert.deepEqual(statusPill({ ...base, bucket: "working" }), { tone: "working", label: "Working" });
    assert.deepEqual(statusPill({ ...base, status: "completed", bucket: "finished" }), { tone: "ok", label: "Done" });
    assert.deepEqual(
      statusPill({
        ...base,
        status: "failed",
        bucket: "finished",
        partial: true,
        error: { code: "task_incomplete", message: "Missing roster.", retryable: false },
      }),
      { tone: "warning", label: "Partial" },
    );
    assert.deepEqual(statusPill({ ...base, status: "failed", bucket: "finished" }), { tone: "danger", label: "Failed" });
  });

  test("Thread renders a single active NoriMark on only the latest Nori reply when idle, and moves it to WorkLine while thinking", () => {
    const items = [
      { id: "m1", sender: "nori" as const, at: Date.parse("2026-11-25T09:00:00Z"), text: "First reply" },
      { id: "m2", sender: "me" as const, at: Date.parse("2026-11-25T09:01:00Z"), text: "Follow up" },
      { id: "m3", sender: "nori" as const, at: Date.parse("2026-11-25T09:02:00Z"), text: "Latest reply" },
    ];

    const idleHtml = renderToStaticMarkup(
      <AssistantProvider localMode>
        <Thread
          conversation={{ title: "Nori", items }}
          emptyMessage="Ask Nori anything"
          hasOlder={false}
          loadingOlder={false}
          onLoadOlder={() => {}}
          onOpenDocument={() => {}}
          onUnread={() => {}}
        />
      </AssistantProvider>,
    );

    const idleActiveMatches = idleHtml.match(/data-active-avatar="true"/g) ?? [];
    assert.equal(idleActiveMatches.length, 1);
    assert.ok(idleHtml.indexOf('data-item-id="m3"') < idleHtml.indexOf('data-active-avatar="true"'));
    // Older Nori message (m1) preserves the w-7 mr-3 alignment spacer so prose stays vertically aligned
    const m1End = idleHtml.indexOf('data-item-id="m2"');
    const m1Slice = idleHtml.slice(idleHtml.indexOf('data-item-id="m1"'), m1End);
    assert.equal(m1Slice.includes("w-7 shrink-0"), true);
    assert.equal(m1Slice.includes('data-active-avatar="true"'), false);
    // Both user and Nori messages support copy
    assert.equal((idleHtml.match(/data-copy-message="true"/g) ?? []).length, 3);

    const sendingHtml = renderToStaticMarkup(
      <AssistantProvider localMode>
        <Thread
          conversation={{
            title: "Nori",
            items: [
              { id: "m_send", sender: "me", at: Date.parse("2026-11-25T09:05:00Z"), text: "Just sent", receipt: "sending" },
            ],
          }}
          emptyMessage="Ask Nori anything"
          hasOlder={false}
          loadingOlder={false}
          onLoadOlder={() => {}}
          onOpenDocument={() => {}}
          onUnread={() => {}}
        />
      </AssistantProvider>,
    );
    assert.match(sendingHtml, /nori-send-arrive/);

    const thinkingHtml = renderToStaticMarkup(
      <AssistantProvider localMode>
        <Thread
          conversation={{ title: "Nori", items }}
          status="Nori is thinking…"
          thinking
          emptyMessage="Ask Nori anything"
          hasOlder={false}
          loadingOlder={false}
          onLoadOlder={() => {}}
          onOpenDocument={() => {}}
          onUnread={() => {}}
        />
      </AssistantProvider>,
    );

    const thinkingActiveMatches = thinkingHtml.match(/data-active-avatar="true"/g) ?? [];
    assert.equal(thinkingActiveMatches.length, 1);
    assert.ok(thinkingHtml.indexOf("Latest reply") < thinkingHtml.indexOf('data-active-avatar="true"'));
    assert.ok(thinkingHtml.includes("data-thinking-indicator"));
    assert.ok(THINKING_WORDS.some((word) => thinkingHtml.includes(word)));
    assert.equal(thinkingHtml.includes("Details"), false);
  });

  test("buildUnits renders engine-segmented cover, page card, and trailing narrative in order", () => {
    const units = buildUnits([
      {
        id: "m-doc",
        sender: "nori",
        at: Date.parse("2026-11-26T15:00:00Z"),
        text: "",
        parts: [
          { type: "text", text: "The task didn't verify the full roster." },
          { type: "page", display: "page", title: "Sudden Impact in Santa Clara", text: "## Preview\nThe NFC West rematch." },
          { type: "text", text: "Want the full-length version pasted in?" },
        ],
      },
    ]);

    const nonSep = units.filter((u) => u.type !== "sep");
    assert.deepEqual(
      nonSep.map((u) => u.type),
      ["group", "page", "group"],
    );
    assert.equal(nonSep[0].type === "group" ? nonSep[0].units[0]?.markdown : "", "The task didn't verify the full roster.");
    assert.equal(nonSep[1].type === "page" ? nonSep[1].title : "", "Sudden Impact in Santa Clara");
    assert.equal(nonSep[2].type === "group" ? nonSep[2].units[0]?.markdown : "", "Want the full-length version pasted in?");
  });

  test("isChatInlineInteraction honors engine surface field and hides work-only questions", () => {
    assert.equal(
      isChatInlineInteraction({
        id: "q_text",
        revision: 1,
        kind: "question",
        surface: "work",
        status: "pending",
        prompt: "What happened?",
        payload: { input: "text" },
      }),
      false,
    );
    assert.equal(
      isChatInlineInteraction({
        id: "q_choice",
        revision: 1,
        kind: "question",
        surface: "chat",
        status: "pending",
        prompt: "Which calendar?",
        payload: { input: "choice", options: [{ id: "work", label: "Work" }] },
      }),
      true,
    );
  });

  test("NoriTabIcon renders active solid mask and inactive outline variants", () => {
    const activeHtml = renderToStaticMarkup(<NoriTabIcon active size={18} />);
    const inactiveHtml = renderToStaticMarkup(<NoriTabIcon active={false} size={18} />);
    assert.match(activeHtml, /<mask/);
    assert.equal(inactiveHtml.includes("<mask"), false);
    assert.match(inactiveHtml, /stroke="currentColor"/);
  });

  test("buildUnifiedTaskEntries orders active, queued, and one-off scheduled tasks without archived runs", () => {
    assert.equal(WORK_HUB_SECTION_LIMIT, 3);
    assert.equal(parseWorkTab(null), "tasks");
    assert.equal(parseWorkTab("artifacts"), "artifacts");
    assert.equal(parseWorkTab("routines"), "automations");
    assert.equal(parseWorkTab("automations"), "automations");

    const baseWork: Omit<WorkItem, "task_id" | "title" | "status" | "bucket"> = {
      instruction_revision: 1,
      origin: { kind: "conversation", conversation_id: "assistant" },
      interaction_ids: [],
      operations: { total: 0, uncertain: 0, failed: 0, submitted: 0 },
      result_ids: [],
      cost: { status: "known", amount_usd: 0.01 },
      started_at: "2026-11-26T10:00:00Z",
      cancel_requested: false,
    };
    const workItems: WorkItem[] = [
      { ...baseWork, task_id: "t-done", title: "Finished old run", status: "completed", bucket: "finished" },
      { ...baseWork, task_id: "t-queued", title: "Queued Q4 brief", status: "running", bucket: "working", queued: true },
      {
        ...baseWork,
        task_id: "t-active",
        title: "Plan Kyoto trip",
        status: "running",
        bucket: "working",
        steps: [{ text: "Compare flights", status: "done" }, { text: "Shortlist ryokans", status: "active" }],
      },
    ];
    const automations: Automation[] = [
      {
        id: "a-once",
        revision: 1,
        title: "Follow up with Sam",
        instruction: "Check SOW",
        status: "enabled",
        trigger: { kind: "once", at: "2026-12-01T17:00:00Z" },
        connection_ids: [],
        authority_rule_ids: [],
        budget_usd: 1,
        max_active_runs: 1,
        missed_run: "latest",
        execution_window_seconds: 3600,
        destination_ids: [],
        next_at: "2026-12-01T17:00:00Z",
      },
      {
        id: "a-recurring",
        revision: 1,
        title: "Morning brief",
        instruction: "Daily summary",
        status: "enabled",
        trigger: { kind: "recurring", local_start: "2026-11-26T08:00", timezone: "UTC", rrule: "FREQ=DAILY" },
        connection_ids: [],
        authority_rule_ids: [],
        budget_usd: 1,
        max_active_runs: 1,
        missed_run: "skip",
        execution_window_seconds: 3600,
        destination_ids: [],
        next_at: "2026-11-27T08:00:00Z",
      },
    ];

    const { entries } = buildUnifiedTaskEntries({ workItems, pendingInteractions: [], automations });
    assert.deepEqual(
      entries.map((entry) => [entry.kind, entry.title]),
      [
        ["in_progress", "Plan Kyoto trip"],
        ["todo", "Queued Q4 brief"],
        ["scheduled", "Follow up with Sam"],
      ],
    );
  });
});
