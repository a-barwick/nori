import assert from "node:assert/strict";
import { describe, test } from "node:test";
import React from "react";
import { renderToStaticMarkup } from "react-dom/server";
import type { Session } from "@/lib/types";
import { TaskPanel } from "./TaskPanel";

function makeSession(): Session {
  return {
    paused: false,
    messages: [],
    runs: [
      {
        id: "run-nova",
        title: "Nova project presentation",
        request: "Create a presentation about Nova, the user's internal enterprise project. Gather details.",
        status: "running",
        cancel_requested: false,
        progress: "Gathering project details",
        started_at: new Date(Date.now() - 120_000).toISOString(),
        events: [{ id: "e1", kind: "model", text: "raw trace", created_at: new Date().toISOString() }],
        artifacts: [],
      },
      {
        id: "run-reminder",
        request: `Set a reminder for the user to check the status on the Nova project "later" (the user's words). If "later" is too vague, ask.`,
        status: "running",
        cancel_requested: false,
        question: {
          id: "q1",
          kind: "input",
          text: 'When would you like to be reminded? "Later" is too vague (America/Los_Angeles).',
          instruction_revision: 1,
        },
        started_at: new Date(Date.now() - 60_000).toISOString(),
        events: [],
        artifacts: [],
      },
      {
        id: "run-done",
        title: "Safeway grocery pickup list",
        request: "Build a Safeway pickup list",
        status: "completed",
        cancel_requested: false,
        started_at: new Date(Date.now() - 600_000).toISOString(),
        finished_at: new Date(Date.now() - 580_000).toISOString(),
        events: [],
        artifacts: [],
      },
      {
        id: "run-partial",
        title: "Seahawks game preview",
        request: "Write Seahawks game preview",
        status: "failed",
        error: "Incomplete. Missing roster details.",
        cancel_requested: false,
        started_at: new Date(Date.now() - 900_000).toISOString(),
        finished_at: new Date(Date.now() - 850_000).toISOString(),
        events: [],
        artifacts: [],
      },
    ],
  };
}

describe("TaskPanel (Activity drawer)", () => {
  test("renders compact Activity header, clean task titles, reply-in-chat, stop button, and zero legacy form clutter", () => {
    const html = renderToStaticMarkup(
      <TaskPanel
        session={makeSession()}
        pending={false}
        loading={false}
        error=""
        unavailable={false}
        onRefresh={() => {}}
        onClose={() => {}}
        onReplyInChat={() => {}}
        commands={[]}
        onCommand={async () => {}}
        onRetryCommand={async () => {}}
        onDismissCommand={() => {}}
      />,
    );

    assert.match(html, />Activity</);
    assert.match(html, /2 running/);
    assert.match(html, /Nova project presentation/);
    assert.match(html, /Set a reminder to check the status on the Nova project &quot;later&quot;/);
    assert.match(html, /Safeway grocery pickup list/);
    assert.match(html, />Partial</);
    assert.match(html, />Done</);
    assert.match(html, /Reply in chat/);
    assert.match(html, /aria-label="Stop Nova project presentation"/);
    assert.match(html, /href="\/work\/run-nova"/);
    assert.match(html, /href="\/work"/);

    // Legacy drawer clutter is gone
    assert.doesNotMatch(html, /View task details/i);
    assert.doesNotMatch(html, /Send task update/i);
    assert.doesNotMatch(html, /Answer this question/i);
    assert.doesNotMatch(html, /Task activity/i);
    assert.doesNotMatch(html, /raw trace/i);
  });
});
