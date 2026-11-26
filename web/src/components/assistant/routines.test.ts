import assert from "node:assert/strict";
import { describe, test } from "node:test";
import { APIError } from "@/lib/assistant/api";
import { localDateString, localTimeString, toValidISOString } from "@/lib/assistant/format";
import { frequencyOf, triggerFrom } from "./RoutineEditor";
import { isRetryableWorkDetailError } from "./WorkDetailScreen";

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
