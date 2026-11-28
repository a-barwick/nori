import assert from "node:assert/strict";
import { afterEach, beforeEach, describe, test } from "node:test";
import React from "react";
import { renderToStaticMarkup } from "react-dom/server";
import { Parts } from "@/components/chat/Parts";
import { Thread } from "@/components/chat/Thread";
import { AssistantProvider } from "@/lib/assistant/context";
import { compactCadence, formatTime12h } from "@/lib/assistant/format";
import { buildReviewApprovalResponse, submitInteractionResponse } from "@/lib/assistant/interactions";
import type { Automation, Interaction, Preferences } from "@/lib/contracts/assistant";
import { asFetch } from "@/lib/test-fetch";
import type { Part } from "@/lib/types";
import { BriefCard, briefItemStatus, day1BodyText } from "./BriefCard";
import { MoreScreen } from "./MoreScreen";
import {
  formatMorningBrief,
  formatQuietHours,
  nextPushPolicy,
  ProactivitySection,
  PUSH_POLICY_LABELS,
  saveProactivityPreferences,
} from "./ProactivitySettings";
import { parseReviewDraft, ReviewApprovalPanel } from "./ReviewApprovalSheet";
import { AutomationRows } from "./RoutinesScreen";

function makeEmailApproval(overrides: Partial<Extract<Interaction, { kind: "approval" }>> = {}): Extract<Interaction, { kind: "approval" }> {
  return {
    id: "int_maya_1",
    revision: 1,
    task_id: "task_maya_1",
    instruction_revision: 1,
    conversation_id: "assistant",
    kind: "approval",
    status: "pending",
    prompt: "Reply to Maya on Q4 pricing",
    action_digest: "sha256:maya_digest_1",
    created_at: "2026-11-27T08:00:00Z",
    payload: {
      summary:
        "Send email\nTo: Maya Lin (maya@acme.co)\nSubject: Re: Q4 Pricing Tier\n\nHi Maya — looks good overall. Let's lock the annual tier at $24k.",
      tool_id: "google/mail/mail-send",
      effect: "sensitive_send",
      account: { connection_id: "conn_gmail", label: "austin@example.com" },
      resources: [],
      recipients: ["maya@acme.co"],
    },
    ...overrides,
  };
}

function makePreferences(overrides: Partial<Preferences> = {}): Preferences {
  return {
    revision: 3,
    timezone: "America/Los_Angeles",
    locale: "en-US",
    working_hours: [{ day: 1, start: "09:00", end: "17:30" }],
    quiet_hours: { enabled: true, start: "21:00", end: "07:30" },
    default_destination_ids: [],
    account_defaults: {},
    budgets: { task_usd: 1, monthly_usd: 20 },
    proactivity: {
      push_policy: "urgent_and_approvals",
      morning_brief_enabled: true,
      morning_brief_time: "08:00",
    },
    ...overrides,
  };
}

function makeAutomation(overrides: Partial<Automation> = {}): Automation {
  return {
    id: "auto_1",
    revision: 1,
    title: "Morning brief",
    instruction: "Compile morning brief",
    status: "enabled",
    trigger: { kind: "recurring", local_start: "2026-11-27T08:00", timezone: "America/Los_Angeles", rrule: "FREQ=DAILY" },
    connection_ids: [],
    authority_rule_ids: [],
    budget_usd: 1,
    max_active_runs: 1,
    missed_run: "skip",
    execution_window_seconds: 3600,
    destination_ids: [],
    next_at: "2026-11-28T08:00:00Z",
    ...overrides,
  };
}

describe("PartBrief (BriefCard) rendering and live interaction status", () => {
  test("renders calm tinted header, single-line rows, and live status pills for pending, approved, superseded, declined, and expired", () => {
    const pending = makeEmailApproval({ id: "int_pending", status: "pending" });
    const approved = makeEmailApproval({
      id: "int_sent",
      status: "resolved",
      resolution: { kind: "approve", at: "2026-11-27T08:05:00Z" },
    });
    const editedApproved = makeEmailApproval({
      id: "int_edited",
      status: "superseded",
      superseded_by: "int_edited_repl",
    });
    const declined = makeEmailApproval({
      id: "int_declined",
      status: "resolved",
      resolution: { kind: "decline", at: "2026-11-27T08:06:00Z" },
    });
    const expired = makeEmailApproval({ id: "int_expired", status: "expired" });

    assert.equal(briefItemStatus(pending), "pending");
    assert.equal(briefItemStatus(approved), "sent");
    assert.equal(briefItemStatus(editedApproved), "sent");
    assert.equal(briefItemStatus(declined), "skipped");
    assert.equal(briefItemStatus(expired), "skipped");
    assert.equal(briefItemStatus(pending, "sent"), "sent");
    assert.equal(briefItemStatus(pending, "skipped"), "skipped");

    const part: Part = {
      type: "brief",
      greeting: "Good morning, Austin",
      meta: "Fri Nov 27 · 58°F",
      items: [
        { title: "Design Sync with Sam", trailing: "2:00 PM" },
        { title: "Reply to Maya on Q4 pricing", interaction_id: "int_pending", action_label: "Review" },
        { title: "Send Q4 summary", interaction_id: "int_sent" },
        { title: "Follow up on invoice", interaction_id: "int_declined" },
      ],
    };

    const html = renderToStaticMarkup(
      <BriefCard
        part={part}
        interactions={{
          int_pending: pending,
          int_sent: approved,
          int_declined: declined,
        }}
      />,
    );

    assert.match(html, /data-brief-card/);
    assert.match(html, /bg-wash-top/);
    assert.match(html, /Good morning, Austin/);
    assert.match(html, /Fri Nov 27 · 58°F/);
    assert.match(html, /Design Sync with Sam/);
    assert.match(html, /2:00 PM/);
    assert.match(html, />Review<\/button>/);
    assert.match(html, />Sent<\/span>/);
    assert.match(html, />Skipped<\/span>/);
    assert.equal(html.toLowerCase().includes("needs you"), false);
  });

  test("renders Day 1 body text when items is empty and integrates via Parts and Thread", () => {
    const emptyBrief: Part = {
      type: "brief",
      greeting: "Good morning, Austin",
      meta: "Fri Nov 27 · 58°F",
      items: [],
      fallback: "Good morning, Austin — Fri Nov 27 · 58°F\nI'll check in like this around 8 AM each morning. What's on your mind today?",
    };

    assert.equal(
      day1BodyText(emptyBrief),
      "I'll check in like this around 8 AM each morning. What's on your mind today?",
    );

    const partsHtml = renderToStaticMarkup(<Parts parts={[emptyBrief]} />);
    assert.match(partsHtml, /data-brief-card/);
    assert.match(partsHtml, /data-brief-empty-body/);
    assert.match(partsHtml, /check in like this around 8 AM each morning/);
    assert.equal(partsHtml.includes("data-brief-row"), false);

    const threadHtml = renderToStaticMarkup(
      <AssistantProvider localMode>
        <Thread
          conversation={{
            title: "Nori",
            items: [{ id: "m_brief", sender: "nori", at: Date.parse("2026-11-27T08:00:00Z"), text: "", parts: [emptyBrief] }],
          }}
          emptyMessage="Ask Nori anything…"
          hasOlder={false}
          loadingOlder={false}
          onLoadOlder={() => {}}
          onOpenDocument={() => {}}
          onUnread={() => {}}
        />
      </AssistantProvider>,
    );
    assert.match(threadHtml, /data-brief-card/);
    assert.equal(threadHtml.match(/Good morning, Austin/g)?.length, 2); // aria-label + h3 only
  });
});

describe("Editable Review modal sheet and payload encoding", () => {
  let origFetch: typeof globalThis.fetch;

  beforeEach(() => {
    origFetch = globalThis.fetch;
  });

  afterEach(() => {
    globalThis.fetch = origFetch;
  });

  test("parseReviewDraft extracts To, Subject, and body from header and inline formats", () => {
    const headerDraft = parseReviewDraft(makeEmailApproval());
    assert.equal(headerDraft.to, "Maya Lin (maya@acme.co)");
    assert.equal(headerDraft.subject, "Re: Q4 Pricing Tier");
    assert.equal(headerDraft.body, "Hi Maya — looks good overall. Let's lock the annual tier at $24k.");

    const inlineDraft = parseReviewDraft(
      makeEmailApproval({
        payload: {
          summary: 'Send "Q4 update" from you@example.com to external@example.com:\nHere is the latest status.',
          tool_id: "google/mail/mail-send",
          effect: "sensitive_send",
          account: { connection_id: "conn_1", label: "you@example.com" },
          resources: [],
          recipients: ["external@example.com"],
        },
      }),
    );
    assert.equal(inlineDraft.to, "external@example.com");
    assert.equal(inlineDraft.subject, "Q4 update");
    assert.equal(inlineDraft.body, "Here is the latest status.");
  });

  test("buildReviewApprovalResponse encodes unmodified approve, edited_fields subject/body approve without remember_rule, and decline on Skip", () => {
    const unmodified = buildReviewApprovalResponse("send", "sha256:d1", "Original body", "Original body", "Re: Q4", "Re: Q4");
    assert.deepEqual(unmodified, { kind: "approve", action_digest: "sha256:d1" });
    assert.equal("edited_fields" in unmodified, false);
    assert.equal("remember_rule" in unmodified, false);

    const editedBody = buildReviewApprovalResponse("send", "sha256:d1", "Original body", "Updated body", "Re: Q4", "Re: Q4");
    assert.deepEqual(editedBody, {
      kind: "approve",
      action_digest: "sha256:d1",
      edited_fields: { body: "Updated body" },
    });
    assert.equal("remember_rule" in editedBody, false);

    const editedSubject = buildReviewApprovalResponse("send", "sha256:d1", "Original body", "Original body", "Re: Q4", "  Re: Q4 Final  ");
    assert.deepEqual(editedSubject, {
      kind: "approve",
      action_digest: "sha256:d1",
      edited_fields: { subject: "Re: Q4 Final" },
    });

    const editedBoth = buildReviewApprovalResponse("send", "sha256:d1", "Original body", "Updated body", "Re: Q4", "Re: Q4 Final");
    assert.deepEqual(editedBoth, {
      kind: "approve",
      action_digest: "sha256:d1",
      edited_fields: { subject: "Re: Q4 Final", body: "Updated body" },
    });

    const skipped = buildReviewApprovalResponse("skip", "sha256:d1", "Original body", "Updated body", "Re: Q4", "Re: Q4 Final");
    assert.deepEqual(skipped, { kind: "decline", action_digest: "sha256:d1" });
    assert.equal("edited_fields" in skipped, false);
  });

  test("ReviewApprovalPanel renders editable Subject and Body, disables Send on empty body, and submits edited_fields wire payload", async () => {
    const interaction = makeEmailApproval({ id: `int_review_${crypto.randomUUID()}` });
    const html = renderToStaticMarkup(
      <ReviewApprovalPanel
        interaction={interaction}
        onClose={() => {}}
        onResolved={() => {}}
      />,
    );

    assert.match(html, /Review reply/);
    assert.match(html, /To:/);
    assert.match(html, /Maya Lin \(maya@acme\.co\)/);
    assert.match(html, /Subject:/);
    assert.match(html, /<input[^>]*aria-label="Subject"[^>]*value="Re: Q4 Pricing Tier"/);
    assert.match(html, /<textarea[^>]*aria-label="Draft reply"/);
    assert.match(html, />Skip<\/button>/);
    assert.match(html, />Send reply<\/button>/);

    const emptyBodyInteraction = makeEmailApproval({
      id: `int_review_empty_${crypto.randomUUID()}`,
      payload: {
        summary: "To: Maya Lin (maya@acme.co)\nSubject: Re: Q4 Pricing Tier\n\n   ",
        tool_id: "google/mail/mail-send",
        effect: "sensitive_send",
        account: { connection_id: "conn_gmail", label: "austin@example.com" },
        resources: [],
        recipients: ["maya@acme.co"],
      },
    });
    const emptyHtml = renderToStaticMarkup(
      <ReviewApprovalPanel
        interaction={emptyBodyInteraction}
        onClose={() => {}}
        onResolved={() => {}}
      />,
    );
    assert.match(emptyHtml, /<button[^>]*disabled=""[^>]*>Send reply<\/button>/);
    assert.equal(/<button[^>]*disabled=""[^>]*>Skip<\/button>/.test(emptyHtml), false);

    const calls: { url: string; method: string; body?: Record<string, unknown> }[] = [];
    globalThis.fetch = asFetch(async (input: RequestInfo | URL, init?: RequestInit) => {
      const url = String(input);
      const method = init?.method ?? "GET";
      const body = typeof init?.body === "string" ? (JSON.parse(init.body) as Record<string, unknown>) : undefined;
      calls.push({ url, method, body });

      if (url === `/api/v1/interactions/${encodeURIComponent(interaction.id)}` && method === "GET") {
        return Response.json(interaction);
      }
      if (url === `/api/v1/interactions/${encodeURIComponent(interaction.id)}/responses` && method === "POST") {
        return Response.json({
          request_id: body?.request_id,
          accepted: true,
          status: "applied",
          resource: {
            ...interaction,
            status: "superseded",
            superseded_by: `${interaction.id}_repl`,
            revision: interaction.revision + 1,
          },
        });
      }
      return Response.json({});
    });

    const payload = buildReviewApprovalResponse(
      "send",
      interaction.action_digest!,
      "Hi Maya — looks good overall. Let's lock the annual tier at $24k.",
      "Hi Maya — looks good. Let's sign at $24k.",
      "Re: Q4 Pricing Tier",
      "Re: Q4 Pricing Tier (Approved)",
    );
    const result = await submitInteractionResponse(interaction, payload);
    assert.equal(result.ok, true);

    const post = calls.find((call) => call.method === "POST");
    assert.ok(post);
    assert.equal(post.url, `/api/v1/interactions/${encodeURIComponent(interaction.id)}/responses`);
    assert.deepEqual(post.body?.value, {
      kind: "approve",
      action_digest: interaction.action_digest,
      edited_fields: {
        subject: "Re: Q4 Pricing Tier (Approved)",
        body: "Hi Maya — looks good. Let's sign at $24k.",
      },
    });
  });
});

describe("Work → Automations single-line row grammar", () => {
  test("renders clock glyph, single-line title, compact cadence, inline × and Remove/Keep confirmation with zero switches or sublabels", () => {
    const items: Automation[] = [
      makeAutomation({ id: "a1", title: "Morning brief" }),
      makeAutomation({
        id: "a2",
        title: "Pre-meeting prep",
        trigger: {
          kind: "event_relative",
          event: { connection_id: "conn_cal", kind: "calendar_event", provider_id: "evt_1" },
          offset_seconds: -900,
        },
      }),
      makeAutomation({
        id: "a3",
        title: "PR #418 review watch",
        trigger: {
          kind: "watch",
          source: { connection_id: "conn_gh", kind: "pull_request", provider_id: "pr_418" },
          predicate_ref: "approvals >= 2",
          min_interval_seconds: 7200,
        },
      }),
    ];

    assert.equal(compactCadence(items[0].trigger), "Daily 8:00 AM");
    assert.equal(compactCadence(items[1].trigger), "15m before");
    assert.equal(compactCadence(items[2].trigger), "Every 2h");

    const defaultHtml = renderToStaticMarkup(
      <AutomationRows
        items={items}
        confirmingId={null}
        onAskConfirm={() => {}}
        onCancelConfirm={() => {}}
        onRemove={() => {}}
      />,
    );

    assert.match(defaultHtml, /Morning brief/);
    assert.match(defaultHtml, /Daily 8:00 AM/);
    assert.match(defaultHtml, /Pre-meeting prep/);
    assert.match(defaultHtml, /15m before/);
    assert.match(defaultHtml, /PR #418 review watch/);
    assert.match(defaultHtml, /Every 2h/);
    assert.match(defaultHtml, /aria-label="Remove Morning brief"/);
    assert.equal(defaultHtml.includes('role="switch"'), false);
    assert.equal(defaultHtml.includes(">Run<"), false);
    assert.equal(defaultHtml.includes(">Pause<"), false);
    assert.equal(/<p[\s>]/.test(defaultHtml), false);

    const confirmHtml = renderToStaticMarkup(
      <AutomationRows
        items={items}
        confirmingId="a1"
        onAskConfirm={() => {}}
        onCancelConfirm={() => {}}
        onRemove={() => {}}
      />,
    );
    assert.match(confirmHtml, />Remove<\/button>/);
    assert.match(confirmHtml, />Keep<\/button>/);
  });
});

describe("More / Settings Proactivity preferences controls", () => {
  let origFetch: typeof globalThis.fetch;

  beforeEach(() => {
    origFetch = globalThis.fetch;
  });

  afterEach(() => {
    globalThis.fetch = origFetch;
  });

  test("formats quiet hours, morning brief, and cycles push policy labels cleanly", () => {
    assert.equal(formatTime12h("08:00"), "8:00 AM");
    assert.equal(formatTime12h("21:00"), "9:00 PM");
    assert.equal(formatTime12h("07:30"), "7:30 AM");
    assert.equal(formatQuietHours({ enabled: true, start: "21:00", end: "07:30" }), "9:00 PM – 7:30 AM");
    assert.equal(formatQuietHours({ enabled: false, start: "21:00", end: "07:30" }), "Off");
    assert.equal(formatMorningBrief({ morning_brief_enabled: true, morning_brief_time: "08:00" }), "8:00 AM");
    assert.equal(formatMorningBrief({ morning_brief_enabled: false, morning_brief_time: "08:00" }), "Off");

    assert.equal(PUSH_POLICY_LABELS.urgent_and_approvals, "Urgent & approvals");
    assert.equal(PUSH_POLICY_LABELS.include_brief, "Include morning brief");
    assert.equal(PUSH_POLICY_LABELS.quiet_only, "Quiet only");
    assert.equal(nextPushPolicy("urgent_and_approvals"), "include_brief");
    assert.equal(nextPushPolicy("include_brief"), "quiet_only");
    assert.equal(nextPushPolicy("quiet_only"), "urgent_and_approvals");
  });

  test("renders 3 clean single-line rows in ProactivitySection and MoreScreen and saves via PUT /v1/preferences", async () => {
    const prefs = makePreferences();
    const sectionHtml = renderToStaticMarkup(<ProactivitySection current={prefs} />);
    assert.match(sectionHtml, /Proactivity/);
    assert.match(sectionHtml, /Quiet hours/);
    assert.match(sectionHtml, /9:00 PM – 7:30 AM/);
    assert.match(sectionHtml, /Push notifications/);
    assert.match(sectionHtml, /Urgent &amp; approvals/);
    assert.match(sectionHtml, /Morning brief/);
    assert.match(sectionHtml, /8:00 AM/);
    assert.equal(sectionHtml.includes('role="switch"'), false);
    assert.equal(sectionHtml.toLowerCase().includes("needs you"), false);

    const moreHtml = renderToStaticMarkup(
      <AssistantProvider localMode>
        <MoreScreen />
      </AssistantProvider>,
    );
    assert.match(moreHtml, /Proactivity/);
    assert.match(moreHtml, /Quiet hours/);
    assert.match(moreHtml, /Push notifications/);
    assert.match(moreHtml, /Morning brief/);

    const calls: { url: string; method: string; body?: Record<string, unknown> }[] = [];
    globalThis.fetch = asFetch(async (input: RequestInfo | URL, init?: RequestInit) => {
      const url = String(input);
      const method = init?.method ?? "GET";
      const body = typeof init?.body === "string" ? (JSON.parse(init.body) as Record<string, unknown>) : undefined;
      calls.push({ url, method, body });
      return Response.json({
        request_id: body?.request_id,
        accepted: true,
        status: "applied",
        resource: {
          ...prefs,
          revision: prefs.revision + 1,
          proactivity: { ...prefs.proactivity, push_policy: "include_brief" },
        },
      });
    });

    const updated = await saveProactivityPreferences(prefs, {
      proactivity: { ...prefs.proactivity, push_policy: "include_brief" },
    });
    assert.equal(updated.revision, prefs.revision + 1);
    assert.equal(updated.proactivity?.push_policy, "include_brief");

    assert.equal(calls.length, 1);
    assert.equal(calls[0].url, "/api/v1/preferences");
    assert.equal(calls[0].method, "PUT");
    assert.equal(calls[0].body?.expected_revision, prefs.revision);
    assert.deepEqual((calls[0].body?.value as Record<string, unknown>)?.proactivity, {
      push_policy: "include_brief",
      morning_brief_enabled: true,
      morning_brief_time: "08:00",
    });
  });
});
