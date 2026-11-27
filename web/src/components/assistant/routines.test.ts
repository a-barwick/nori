import assert from "node:assert/strict";
import { describe, test } from "node:test";
import { APIError } from "@/lib/assistant/api";
import { localDateString, localTimeString, toValidISOString } from "@/lib/assistant/format";
import { frequencyOf, triggerFrom } from "./RoutineEditor";
import {
  budgetPresets,
  buildTaskSteps,
  externalChangeOperations,
  formatDuration,
  isBudgetLimitedTask,
  isLongMarkdown,
  isRetryableWorkDetailError,
  selectPrimaryArtifact,
  stripLeadingMatchingHeading,
} from "./WorkDetailScreen";
import type { LibraryItem, OperationProjection, RunEvent } from "@/lib/contracts/assistant";

describe("RoutineEditor date/time helpers", () => {
  test("frequencyOf uses local calendar date and local time consistently for one-time triggers", () => {
    const sampleTimestamps = [
      "2026-06-15T01:30:00Z",
      "2026-06-15T23:45:00Z",
      "2026-12-31T23:30:00Z",
      "2027-01-01T00:15:00Z",
    ];
    for (const at of sampleTimestamps) {
      const parsed = new Date(at);
      const result = frequencyOf({ kind: "once", at });
      assert.equal(result.frequency, "once");
      assert.equal(result.date, localDateString(parsed));
      assert.equal(result.time, localTimeString(parsed));

      // Re-constructing a once trigger from the local date + local time round-trips to the exact same instant.
      const rebuilt = triggerFrom("once", result.date, result.time, [], "UTC");
      assert.ok(rebuilt && rebuilt.kind === "once");
      assert.equal(new Date(rebuilt.at).getTime(), parsed.getTime());
    }
  });

  test("frequencyOf falls back cleanly on invalid once timestamps and uses localDateString for default date", () => {
    const now = new Date(2026, 5, 15, 23, 30, 0);
    const invalid = frequencyOf({ kind: "once", at: "not-a-date" }, now);
    assert.equal(invalid.frequency, "once");
    assert.equal(invalid.date, localDateString(now));
    assert.equal(invalid.time, "08:00");

    const fallback = frequencyOf(undefined, now);
    assert.equal(fallback.frequency, "weekdays");
    assert.equal(fallback.date, localDateString(now));
  });

  test("triggerFrom returns undefined instead of throwing on invalid or partial once dates", () => {
    assert.equal(triggerFrom("once", "", "08:00", [], "UTC"), undefined);
    assert.equal(triggerFrom("once", "2026-06-15", "", [], "UTC"), undefined);
    assert.equal(triggerFrom("once", "2026-99-99", "99:99", [], "UTC"), undefined);
    assert.equal(toValidISOString("not-a-date"), undefined);
    assert.equal(toValidISOString("2026-06-15T12:00:00Z"), "2026-06-15T12:00:00.000Z");
  });
});

describe("isRetryableWorkDetailError", () => {
  test("retries transient/fresh-404 API errors and does not retry permanent 4xx or 500 errors", () => {
    for (const status of [404, 502, 503, 504, 0]) {
      assert.equal(isRetryableWorkDetailError(new APIError("transient", status)), true);
    }
    for (const status of [400, 401, 403, 409, 422, 500]) {
      assert.equal(isRetryableWorkDetailError(new APIError("permanent", status)), false);
    }
    assert.equal(isRetryableWorkDetailError(new Error("generic")), false);
    assert.equal(isRetryableWorkDetailError(undefined), false);
  });
});

describe("WorkDetailScreen redesign helpers", () => {
  test("selectPrimaryArtifact promotes a single text document and keeps CSVs/files/multi-artifacts separate", () => {
    const baseDoc: LibraryItem = {
      id: "art_1",
      root_id: "art_1",
      version: 1,
      revision: 1,
      kind: "document",
      title: "Overnight Sourdough Focaccia",
      media_type: "text/markdown",
      provenance: { content: "generated" },
      created_at: "2026-11-26T09:17:00Z",
    };

    assert.equal(selectPrimaryArtifact([baseDoc], "Recipe body")?.id, "art_1");
    assert.equal(selectPrimaryArtifact([baseDoc], ""), null);
    assert.equal(selectPrimaryArtifact([baseDoc, { ...baseDoc, id: "art_2" }], "Recipe body"), null);
    assert.equal(selectPrimaryArtifact([{ ...baseDoc, media_type: "text/csv" }], "Summary"), null);
    assert.equal(selectPrimaryArtifact([{ ...baseDoc, kind: "file", media_type: "application/pdf" }], "Summary"), null);
  });

  test("stripLeadingMatchingHeading removes duplicate H1-H3 titles but preserves distinct headings", () => {
    assert.equal(
      stripLeadingMatchingHeading("### Overnight Focaccia\n\nMix flour and water.", "Overnight Focaccia"),
      "Mix flour and water.",
    );
    assert.equal(
      stripLeadingMatchingHeading("## Ingredients\n\n- 500g flour", "Overnight Focaccia"),
      "## Ingredients\n\n- 500g flour",
    );
  });

  test("externalChangeOperations keeps writes/sends and omits routine reads and capability_load", () => {
    const ops: OperationProjection[] = [
      { id: "c0", call_id: "c0", name: "capability_load", effect_class: "control", outcome: "confirmed", revision: 1 },
      { id: "r1", call_id: "r1", name: "web_search", effect_class: "read", outcome: "confirmed", revision: 1 },
      { id: "w1", call_id: "w1", name: "notes_append", effect_class: "reversible_write", outcome: "confirmed", revision: 1, action_summary: "Added 4 items" },
      { id: "s1", call_id: "s1", name: "gmail_send", effect_class: "sensitive_send", outcome: "uncertain", revision: 1, action_summary: "Send email" },
    ];
    const changes = externalChangeOperations(ops);
    assert.deepEqual(
      changes.map((op) => op.id),
      ["w1", "s1"],
    );
  });

  test("buildTaskSteps unifies grouped searches, operations, and non-duplicate events chronologically", () => {
    const ops: OperationProjection[] = [
      { id: "c0", call_id: "c0", name: "capability_load", effect_class: "control", outcome: "confirmed", revision: 1, created_at: "2026-11-26T09:15:00Z" },
      { id: "s1", call_id: "s1", name: "web_search", effect_class: "read", outcome: "confirmed", revision: 1, action_summary: "Query 1", created_at: "2026-11-26T09:15:10Z" },
      { id: "s2", call_id: "s2", name: "web_search", effect_class: "read", outcome: "confirmed", revision: 1, action_summary: "Query 2", created_at: "2026-11-26T09:15:20Z" },
      { id: "w1", call_id: "w1", name: "notes_append", effect_class: "reversible_write", outcome: "confirmed", revision: 1, action_summary: "Updated grocery note", created_at: "2026-11-26T09:16:00Z" },
    ];
    const events: RunEvent[] = [
      { kind: "tool", text: "web_search", created_at: "2026-11-26T09:15:10Z" },
      { kind: "progress", text: "Updated grocery note", created_at: "2026-11-26T09:16:00Z" },
      { kind: "progress", text: "Saved recipe to Artifacts", created_at: "2026-11-26T09:17:00Z" },
    ];

    const steps = buildTaskSteps(ops, events);
    assert.equal(steps.length, 3);
    assert.equal(steps[0]?.kind, "search_group");
    assert.equal(steps[0]?.title, "Searched the web (2 queries)");
    assert.equal(steps[1]?.title, "Updated grocery note");
    assert.equal(steps[2]?.title, "Saved recipe to Artifacts");
  });

  test("budgetPresets, isBudgetLimitedTask, isLongMarkdown, and formatDuration behave consistently", () => {
    assert.deepEqual(budgetPresets(1), [2, 5, 10]);
    assert.deepEqual(budgetPresets(10), [15, 20, 30]);
    assert.equal(
      isBudgetLimitedTask({
        status: "failed",
        cost: { status: "known", amount_usd: 1.0, budget_usd: 1.0 },
        error: { code: "budget", message: "Reached spending limit", retryable: false },
      }),
      true,
    );
    assert.equal(isLongMarkdown("Short summary."), false);
    assert.equal(isLongMarkdown("Line\n".repeat(12)), true);
    assert.equal(formatDuration("2026-11-26T09:15:00Z", "2026-11-26T09:16:34Z"), "1m 34s");
  });
});
