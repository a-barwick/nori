import assert from "node:assert/strict";
import { afterEach, beforeEach, describe, test } from "node:test";
import React from "react";
import { renderToStaticMarkup } from "react-dom/server";
import { Thread } from "@/components/chat/Thread";
import { AssistantProvider } from "@/lib/assistant/context";
import type { Automation, Interaction, OperationProjection } from "@/lib/contracts/assistant";
import type { Part, TextItem } from "@/lib/types";
import { buildUnits } from "@/lib/units";
import { InteractionCard } from "./InteractionCard";
import { cancelReminder, formatReminderTime, ReminderCard } from "./ReminderCard";
import { groupOperations, operationOutcomePill, OperationRows } from "./WorkScreen";

function makeSearchOp(id: string, summary: string, overrides: Partial<OperationProjection> = {}): OperationProjection {
  return {
    id,
    call_id: `ws_${id}`,
    name: "web_search",
    effect_class: "read",
    outcome: "confirmed",
    revision: 1,
    action_summary: summary,
    created_at: "2026-11-25T06:49:10Z",
    owner: { type: "task", id: "task_weather" },
    ...overrides,
  };
}

describe("Fix 1 & 3 (Web): Inline Reminder Confirmation Card and Schedule Confirmation Card", () => {
  let origFetch: typeof globalThis.fetch;

  beforeEach(() => {
    origFetch = globalThis.fetch;
  });

  afterEach(() => {
    globalThis.fetch = origFetch;
  });

  test("formatReminderTime formats today/tomorrow cleanly and falls back to server text", () => {
    const now = new Date(2026, 10, 24, 15, 0, 0).getTime();
    const todayNoon = new Date(2026, 10, 24, 12, 0, 0).toISOString();
    const tomorrowNoon = new Date(2026, 10, 25, 12, 0, 0).toISOString();
    const nextWeek = new Date(2026, 11, 5, 9, 0, 0).toISOString();

    assert.match(formatReminderTime({ text: "Tue, Nov 24 at 12:00 PM", scheduled_at: todayNoon }, now), /^Today at 12:00\s*PM$/);
    assert.match(formatReminderTime({ text: "Wed, Nov 25 at 12:00 PM", scheduled_at: tomorrowNoon }, now), /^Tomorrow at 12:00\s*PM$/);
    assert.equal(formatReminderTime({ text: "Sat, Dec 5 at 9:00 AM PST", scheduled_at: nextWeek }, now), "Sat, Dec 5 at 9:00 AM PST");
    assert.equal(formatReminderTime({ text: "Wed, Nov 25 at 12:00 PM PST" }, now), "Wed, Nov 25 at 12:00 PM PST");
  });

  test("ReminderCard and Thread render inline reminder part with Edit and Cancel actions", () => {
    const reminderPart: Part = {
      type: "reminder",
      id: "auto_1234567890abcdef1234",
      name: "Check supernino updates",
      text: "Wed, Nov 25 at 12:00 PM PST",
      scheduled_at: "2026-11-25T20:00:00Z",
      fallback: "Reminder set · Wed, Nov 25 at 12:00 PM PST · Check supernino updates",
    };

    const cardHtml = renderToStaticMarkup(<ReminderCard part={reminderPart} />);
    assert.match(cardHtml, /data-reminder-card/);
    assert.match(cardHtml, /Reminder set/);
    assert.match(cardHtml, /Check supernino updates/);
    assert.match(cardHtml, /\/routines\/auto_1234567890abcdef1234\/edit/);
    assert.match(cardHtml, />Edit</);
    assert.match(cardHtml, />Cancel</);

    const threadHtml = renderToStaticMarkup(
      <AssistantProvider localMode>
        <Thread
          conversation={{
            title: "Nori",
            items: [
              {
                id: "m_rem_1",
                sender: "nori",
                at: Date.parse("2026-11-25T06:56:00Z"),
                text: "",
                parts: [reminderPart],
              },
            ],
          }}
          emptyMessage="Say hello"
          hasOlder={false}
          loadingOlder={false}
          onLoadOlder={() => {}}
          onOpenDocument={() => {}}
          onUnread={() => {}}
        />
      </AssistantProvider>,
    );
    assert.match(threadHtml, /data-reminder-card/);
    assert.match(threadHtml, /Check supernino updates/);
  });

  test("cancelReminder reads current automation revision, posts cancel action, and invalidates caches", async () => {
    const id = `auto_cancel_${crypto.randomUUID()}`;
    const calls: { url: string; method: string; body?: Record<string, unknown> }[] = [];
    const automation: Automation = {
      id,
      revision: 2,
      title: "Check supernino updates",
      instruction: "Remind me to check supernino updates.",
      status: "enabled",
      trigger: { kind: "once", at: "2026-11-25T20:00:00Z" },
      connection_ids: [],
      authority_rule_ids: [],
      budget_usd: 0.25,
      max_active_runs: 1,
      missed_run: "latest",
      execution_window_seconds: 3600,
      destination_ids: [],
    };


    globalThis.fetch = (async (input: RequestInfo | URL, init?: RequestInit) => {
      const url = String(input);
      const method = init?.method ?? "GET";
      const body = typeof init?.body === "string" ? (JSON.parse(init.body) as Record<string, unknown>) : undefined;
      calls.push({ url, method, body });

      if (url === `/api/v1/automations/${encodeURIComponent(id)}` && method === "GET") {
        return Response.json(automation);
      }
      if (url === `/api/v1/automations/${encodeURIComponent(id)}/actions` && method === "POST") {
        return Response.json({
          request_id: body?.request_id,
          accepted: true,
          status: "applied",
          resource: { ...automation, status: "cancelled", revision: 3 },
        });
      }
      return Response.json({ items: [] });
    }) as unknown as typeof fetch;

    const invalidated: string[] = [];
    const result = await cancelReminder(id, (...prefixes) => invalidated.push(...prefixes));

    assert.equal(result.status, "cancelled");
    assert.equal(result.revision, 3);
    const post = calls.find((call) => call.method === "POST");
    assert.ok(post);
    assert.equal(post.body?.expected_revision, 2);
    assert.deepEqual(post.body?.value, { action: "cancel" });
    assert.equal(invalidated.includes("/automations"), true);
    assert.equal(invalidated.includes("/assistant/summary"), true);
  });

  test("Schedule Confirmation card shows calm title, strips external-records warning for internal schedules, and hides empty account and raw tool_id", () => {
    const scheduleApproval: Interaction = {
      id: "int_sched_1",
      revision: 1,
      task_id: "task_sched_1",
      instruction_revision: 1,
      kind: "approval",
      status: "pending",
      prompt: "Create this routine?",
      action_digest: "sha256:sched1",
      created_at: "2026-11-25T06:56:15Z",
      payload: {
        summary: "Daily standup prep\nEvery weekday at 9:00 AM PST · Next: Thu, Nov 26 at 9:00 AM PST",
        tool_id: "nori/assistant/automation-create",
        warning: "This modifies external records.",
        effect: "reversible_write",
        account: { connection_id: "", label: "" },
        resources: [],
      },
    };

    const html = renderToStaticMarkup(
      <AssistantProvider localMode>
        <InteractionCard interaction={scheduleApproval} inline />
      </AssistantProvider>,
    );

    assert.match(html, /Create this routine\?/);
    assert.match(html, /Daily standup prep/);
    assert.equal(html.includes("This modifies external records"), false);
    assert.equal(html.includes("data-approval-warning"), false);
    assert.equal(html.includes("nori/assistant/automation-create"), false);
    assert.equal(html.includes(">Account<"), false);
  });
});

describe("Fix 2 (Web): Work Detail Actions grouping and read badge suppression", () => {
  test("operationOutcomePill suppresses green Confirmed pill on routine reads but keeps it on writes/sends and shows failures", () => {
    assert.equal(operationOutcomePill({ effect_class: "read", outcome: "confirmed" }), undefined);
    assert.deepEqual(operationOutcomePill({ effect_class: "read", outcome: "failed" }), { tone: "danger", label: "Failed" });
    assert.deepEqual(operationOutcomePill({ effect_class: "read", outcome: "attempted" }), { tone: "neutral", label: "In progress" });
    assert.deepEqual(operationOutcomePill({ effect_class: "reversible_write", outcome: "confirmed" }), { tone: "ok", label: "Confirmed" });
    assert.deepEqual(operationOutcomePill({ effect_class: "sensitive_send", outcome: "confirmed" }), { tone: "ok", label: "Confirmed" });
    assert.deepEqual(operationOutcomePill({ effect_class: "read", outcome: "failed", cleared: "read_only" }), { tone: "neutral", label: "Only read" });
  });

  test("groupOperations groups consecutive web searches and preserves chronological order around other actions", () => {
    const ops: OperationProjection[] = [
      makeSearchOp("ws1", "Searched: Austin weather Thursday"),
      makeSearchOp("ws2", "Searched: Austin forecast rain"),
      {
        id: "op_cal",
        call_id: "c1",
        name: "calendar_create_event",
        effect_class: "reversible_write",
        outcome: "confirmed",
        revision: 1,
        action_summary: "Created 'Umbrella reminder'",
        account_summary: "you@example.com",
        authority_source: "user_request",
      },
      makeSearchOp("ws3", "Read page: https://weather.gov/austin"),
    ];

    const grouped = groupOperations(ops);
    assert.equal(grouped.length, 3);
    assert.equal(grouped[0].kind, "search_group");
    if (grouped[0].kind === "search_group") {
      assert.equal(grouped[0].operations.length, 2);
    }
    assert.equal(grouped[1].kind, "single");
    assert.equal(grouped[2].kind, "single");

    const html = renderToStaticMarkup(<OperationRows operations={ops} />);
    assert.match(html, /data-search-group/);
    assert.match(html, /Searched the web \(2 queries\)/);
    assert.match(html, /Read page: https:\/\/weather\.gov\/austin/);
    assert.match(html, /Created &#x27;Umbrella reminder&#x27;/);
    assert.equal(html.match(/>Confirmed</g)?.length, 1);
    assert.equal(html.includes("because Nori"), false);
  });
});

describe("Fix 5 (Web): Typed chat error card and Retry affordance", () => {
  test("buildUnits separates failed Nori messages into standalone error units", () => {
    const items: TextItem[] = [
      { id: "m1", sender: "me", at: 1000, text: "Check weather" },
      {
        id: "m2",
        sender: "nori",
        at: 2000,
        status: "failed",
        turnId: "turn-1",
        text: "Nori's model service is temporarily busy.",
        error: { code: "provider_overloaded", message: "Nori's model service is temporarily busy.", retryable: true },
      },
    ];
    const units = buildUnits(items);
    const errorUnits = units.filter((u) => u.type === "error");
    assert.equal(errorUnits.length, 1);
    assert.equal(errorUnits[0].item.id, "m2");
  });

  test("Thread renders clean error card with Retry button when retryable and omits Retry when non-retryable", () => {
    const retryableHtml = renderToStaticMarkup(
      <AssistantProvider localMode>
        <Thread
          conversation={{
            title: "Nori",
            items: [
              { id: "u1", sender: "me", at: Date.parse("2026-11-25T07:01:16Z"), text: "What's up?" },
              {
                id: "t1-assistant",
                sender: "nori",
                at: Date.parse("2026-11-25T07:01:18Z"),
                status: "failed",
                turnId: "t1",
                text: "Nori's model service is temporarily busy. Your message is saved — tap Retry to try again.",
                error: {
                  code: "provider_overloaded",
                  message: "Nori's model service is temporarily busy. Your message is saved — tap Retry to try again.",
                  retryable: true,
                },
              },
            ],
          }}
          emptyMessage="Say hello"
          hasOlder={false}
          loadingOlder={false}
          onLoadOlder={() => {}}
          onOpenDocument={() => {}}
          onUnread={() => {}}
          onRetryMessage={() => {}}
        />
      </AssistantProvider>,
    );

    assert.match(retryableHtml, /data-message-error/);
    assert.match(retryableHtml, /data-error-code="provider_overloaded"/);
    assert.match(retryableHtml, /Reply didn’t go through/);
    assert.match(retryableHtml, /temporarily busy/);
    assert.match(retryableHtml, />Retry</);

    const nonRetryableHtml = renderToStaticMarkup(
      <AssistantProvider localMode>
        <Thread
          conversation={{
            title: "Nori",
            items: [
              {
                id: "t2-assistant",
                sender: "nori",
                at: Date.parse("2026-11-25T07:02:00Z"),
                status: "failed",
                turnId: "t2",
                text: "That conversation grew too long to process at once.",
                error: {
                  code: "context_too_long",
                  message: "That conversation grew too long to process at once.",
                  retryable: false,
                },
              },
            ],
          }}
          emptyMessage="Say hello"
          hasOlder={false}
          loadingOlder={false}
          onLoadOlder={() => {}}
          onOpenDocument={() => {}}
          onUnread={() => {}}
          onRetryMessage={() => {}}
        />
      </AssistantProvider>,
    );

    assert.match(nonRetryableHtml, /data-message-error/);
    assert.equal(nonRetryableHtml.includes(">Retry<"), false);
  });
});
