import assert from "node:assert/strict";
import { describe, test } from "node:test";
import { cadence, EFFECT_LABEL, eventTime, money, plainText, plural, relative } from "./format";

describe("plainText", () => {
  test("strips markdown links and image syntax while keeping labels", () => {
    assert.equal(plainText("See [quarterly report](https://example.com/q4) now"), "See quarterly report now");
    assert.equal(plainText("Chart: ![Q4 revenue](https://example.com/chart.png)"), "Chart: Q4 revenue");
  });

  test("strips headings, unordered lists, ordered lists, and blockquotes", () => {
    const md = [
      "# Main title",
      "### Subheading",
      "- Bullet one",
      "* Bullet two",
      "+ Bullet three",
      "1. First step",
      "2. Second step",
      "> Quoted note",
    ].join("\n");
    assert.equal(plainText(md), "Main title Subheading Bullet one Bullet two Bullet three First step Second step Quoted note");
  });

  test("strips bold, strikethrough, inline code, and stray double markers", () => {
    assert.equal(plainText("**Bold**, __strong__, ~~old~~, and `npm test`"), "Bold, strong, old, and npm test");
    assert.equal(plainText("Unclosed **bold or __under marker"), "Unclosed bold or under marker");
  });

  test("collapses multiline markdown and trims surrounding whitespace", () => {
    const md = "  ## Summary  \n\n  - Review **draft** with [Alice](mailto:a@example.com)\n  - Run `make check`  \n";
    assert.equal(plainText(md), "Summary Review draft with Alice Run make check");
  });

  test("preserves snake_case identifiers and single emphasis characters", () => {
    assert.equal(plainText("Check `run_id` and task_status_v2 *carefully*"), "Check run_id and task_status_v2 *carefully*");
  });

  test("handles code-fenced blocks into a single trimmed line", () => {
    const fenced = "Result:\n```\nconst ok = true;\n```";
    const out = plainText(fenced);
    assert.equal(out.includes("\n"), false);
    assert.equal(out.includes("const ok = true;"), true);
  });
});

describe("format helpers", () => {
  test("relative formats near-now, minutes, hours, and days deterministically", () => {
    const now = Date.parse("2026-06-15T12:00:00Z");
    assert.equal(relative(undefined, now), "");
    assert.equal(relative("2026-06-15T12:00:10Z", now), "now");
    assert.equal(relative("2026-06-15T11:59:40Z", now), "just now");
    assert.equal(relative("2026-06-15T12:20:00Z", now), "in 20 minutes");
    assert.equal(relative("2026-06-15T09:00:00Z", now), "3 hours ago");
    assert.equal(relative("2026-06-14T12:00:00Z", now), "yesterday");
  });

  test("money formats sub-cent, regular, zero, and undefined values", () => {
    assert.equal(money(undefined), "");
    assert.equal(money(0), "$0.00");
    assert.equal(money(0.004), "<$0.01");
    assert.equal(money(0.01), "$0.01");
    assert.equal(money(12.5), "$12.50");
  });

  test("plural handles singular, plural, and custom plural forms", () => {
    assert.equal(plural(1, "task"), "1 task");
    assert.equal(plural(0, "task"), "0 tasks");
    assert.equal(plural(3, "item"), "3 items");
    assert.equal(plural(2, "entry", "entries"), "2 entries");
  });

  test("cadence formats recurring, watch, and event-relative triggers", () => {
    const ref = { connection_id: "conn-1", kind: "calendar", provider_id: "primary" };
    assert.equal(
      cadence({ kind: "recurring", rrule: "FREQ=DAILY", local_start: "2026-06-15T09:30:00", timezone: "UTC" }),
      "Every day at 09:30",
    );
    assert.equal(
      cadence({ kind: "recurring", rrule: "FREQ=WEEKLY;BYDAY=MO,TU,WE,TH,FR", local_start: "2026-06-15T08:00:00", timezone: "UTC" }),
      "Weekdays at 08:00",
    );
    assert.equal(
      cadence({ kind: "recurring", rrule: "FREQ=WEEKLY;BYDAY=MO,WE", local_start: "2026-06-15T10:15:00", timezone: "UTC" }),
      "Mon, Wed at 10:15",
    );
    assert.equal(
      cadence({ kind: "event_relative", event: ref, offset_seconds: -900 }),
      "15 min before an event",
    );
    assert.equal(
      cadence({ kind: "event_relative", event: ref, offset_seconds: 7200 }),
      "2 h after an event",
    );
    assert.equal(
      cadence({ kind: "watch", source: { ...ref, kind: "mail" }, predicate_ref: "unread", min_interval_seconds: 600 }),
      "Checks for changes every 10 min or more",
    );
  });

  test("eventTime formats single all-day and timed UTC events", () => {
    const singleDay = eventTime({ kind: "all_day", start_date: "2026-06-15", end_date: "2026-06-16" });
    assert.equal(singleDay.endsWith("· all day"), true);
    const timed = eventTime({ kind: "timed", start: "2026-06-15T09:00:00Z", end: "2026-06-15T10:30:00Z", timezone: "UTC" });
    assert.match(timed, /0?9:00–10:30/);
    assert.equal(EFFECT_LABEL.sensitive_send, "Send");
  });
});
