import assert from "node:assert/strict";
import { describe, test } from "node:test";
import type { Preferences } from "@/lib/contracts/assistant";
import { buildOnboardingMemoryFacts, shouldShowOnboarding } from "./OnboardingFlow";

const basePreferences: Preferences = {
  revision: 1,
  timezone: "America/Los_Angeles",
  working_hours: [{ day: 1, start: "09:00", end: "17:00" }],
  default_destination_ids: [],
  account_defaults: {},
  budgets: { task_usd: 5 },
};

describe("shouldShowOnboarding", () => {
  test("shows for brand-new users with no messages and no completion timestamp", () => {
    assert.equal(
      shouldShowOnboarding({
        forceReplay: false,
        preferences: basePreferences,
        preferencesLoaded: true,
        messageCount: 0,
        sessionLoaded: true,
        localCompleted: false,
      }),
      true,
    );
  });

  test("does not show for existing users who already have messages", () => {
    assert.equal(
      shouldShowOnboarding({
        forceReplay: false,
        preferences: basePreferences,
        preferencesLoaded: true,
        messageCount: 3,
        sessionLoaded: true,
        localCompleted: false,
      }),
      false,
    );
  });

  test("does not show once onboarding_completed_at is persisted", () => {
    assert.equal(
      shouldShowOnboarding({
        forceReplay: false,
        preferences: { ...basePreferences, onboarding_completed_at: "2026-11-25T18:50:00Z" },
        preferencesLoaded: true,
        messageCount: 0,
        sessionLoaded: true,
        localCompleted: false,
      }),
      false,
    );
  });

  test("shows after a reset even if a new intro message exists", () => {
    assert.equal(
      shouldShowOnboarding({
        forceReplay: false,
        preferences: { ...basePreferences, data_reset_at: "2026-11-25T18:50:00Z" },
        preferencesLoaded: true,
        messageCount: 1,
        sessionLoaded: true,
        localCompleted: false,
      }),
      true,
    );
  });

  test("shows when forceReplay is requested", () => {
    assert.equal(
      shouldShowOnboarding({
        forceReplay: true,
        preferences: { ...basePreferences, onboarding_completed_at: "2026-11-25T18:50:00Z" },
        preferencesLoaded: true,
        messageCount: 10,
        sessionLoaded: true,
        localCompleted: true,
      }),
      true,
    );
  });
});

describe("buildOnboardingMemoryFacts", () => {
  test("builds clean Preferences and Context facts from user inputs", () => {
    assert.deepEqual(
      buildOnboardingMemoryFacts({
        name: "Austin",
        replyStyle: "Concise & direct",
        focus: ["Engineering", "Daily planning"],
        workingOn: "Nori onboarding",
      }),
      [
        { section: "Preferences", text: "The user's name is Austin." },
        { section: "Preferences", text: "Prefers concise & direct replies.", pinned: true },
        { section: "Context", text: "Uses Nori for engineering, daily planning." },
        { section: "Context", text: "Currently working on: Nori onboarding" },
      ],
    );
  });

  test("omits empty fields cleanly", () => {
    assert.deepEqual(
      buildOnboardingMemoryFacts({
        name: "  ",
        replyStyle: "",
        focus: [],
        workingOn: "",
      }),
      [],
    );
  });
});
