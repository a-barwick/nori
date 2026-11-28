import assert from "node:assert/strict";
import { test } from "node:test";
import { desktopConnectionPill } from "./ComputerDesktopDrawer";
import { computerStatusPill } from "./ComputerScreen";
import { resolvedStatusPill, resolvedSummaryText, responseAnnouncement } from "@/lib/assistant/interactions";
import type { Interaction } from "@/lib/contracts/assistant";

test("maps computer statuses to calm, jargon-free labels", () => {
  assert.deepEqual(computerStatusPill("ready"), { tone: "ok", label: "Active" });
  assert.deepEqual(computerStatusPill("sleeping"), { tone: "neutral", label: "Asleep" });
  assert.deepEqual(computerStatusPill("not_provisioned"), { tone: "neutral", label: "Ready to start" });
  assert.deepEqual(computerStatusPill("provisioning"), { tone: "working", label: "Starting" });
  assert.deepEqual(computerStatusPill("error"), { tone: "danger", label: "Needs attention" });
});

test("maps desktop drawer connection states without falling back to Starting desktop on disconnect", () => {
  assert.deepEqual(desktopConnectionPill("connecting", "control"), { tone: "warning", label: "Starting desktop…" });
  assert.deepEqual(desktopConnectionPill("reconnecting", "control"), { tone: "warning", label: "Reconnecting…" });
  assert.deepEqual(desktopConnectionPill("connected", "view"), { tone: "ok", label: "Watching" });
  assert.deepEqual(desktopConnectionPill("connected", "control"), { tone: "working", label: "In control" });
  assert.deepEqual(desktopConnectionPill("error", "control"), { tone: "danger", label: "Disconnected" });
  assert.deepEqual(desktopConnectionPill("disconnected", "control"), { tone: "danger", label: "Disconnected" });
});

test("handles computer_takeover resume and cancel resolutions", () => {
  assert.equal(responseAnnouncement({ kind: "resume" }), "Handed back to Nori.");
  assert.equal(responseAnnouncement({ kind: "cancel" }), "Takeover cancelled.");

  const resumed: Interaction = {
    id: "int_takeover_1",
    revision: 2,
    task_id: "task_1",
    kind: "computer_takeover",
    status: "resolved",
    prompt: "Sign into Gmail on mail.google.com, then click Done.",
    payload: { reason: "login", url: "https://mail.google.com" },
    resolution: { kind: "resume", text: "Signed in", at: "2026-11-27T12:00:00Z" },
  };
  assert.deepEqual(resolvedStatusPill(resumed), { tone: "ok", label: "Resumed" });
  assert.equal(resolvedSummaryText(resumed), "Resumed · Signed in");
});
