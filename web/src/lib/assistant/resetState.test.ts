import assert from "node:assert/strict";
import { describe, test } from "node:test";
import { installTestDOM } from "./test-dom";
import { browserResetChanged, resetMarkerKey, syncBrowserReset } from "./resetState";

describe("browser reset state", () => {
  test("clears this tab's pending work when another tab already observed the reset", () => {
    const restore = installTestDOM();
    try {
      localStorage.setItem(resetMarkerKey("alice"), "new-reset");
      localStorage.setItem("nori-draft-assistant-v1:alice", "a new draft");
      sessionStorage.setItem(resetMarkerKey("alice"), "old-reset");
      sessionStorage.setItem("nori-pending-turns-v1:alice", "old turn");
      sessionStorage.setItem("nori-pending-task-commands-v1:alice", "old command");

      assert.equal(browserResetChanged("alice", "new-reset"), true);
      syncBrowserReset("alice", "new-reset");

      assert.equal(sessionStorage.getItem("nori-pending-turns-v1:alice"), null);
      assert.equal(sessionStorage.getItem("nori-pending-task-commands-v1:alice"), null);
      assert.equal(localStorage.getItem("nori-draft-assistant-v1:alice"), "a new draft");
      assert.equal(browserResetChanged("alice", "new-reset"), false);
    } finally { restore(); }
  });

  test("clears previous local state on a reset without touching another account", () => {
    const restore = installTestDOM();
    try {
      localStorage.setItem("nori.onboarding.done:alice", "1");
      localStorage.setItem("nori-draft-assistant-v1:alice", "old draft");
      localStorage.setItem("nori-diagnostics-model-selection-v1:alice", '{"chat_profile_id":"old"}');
      localStorage.setItem("nori-draft-assistant-v1:bob", "bob draft");
      syncBrowserReset("alice", "new-reset");
      assert.equal(localStorage.getItem("nori.onboarding.done:alice"), null);
      assert.equal(localStorage.getItem("nori-draft-assistant-v1:alice"), null);
      assert.equal(localStorage.getItem("nori-diagnostics-model-selection-v1:alice"), null);
      assert.equal(localStorage.getItem("nori-draft-assistant-v1:bob"), "bob draft");
    } finally { restore(); }
  });
});
