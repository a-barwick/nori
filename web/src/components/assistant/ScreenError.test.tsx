import assert from "node:assert/strict";
import { afterEach, beforeEach, describe, test } from "node:test";
import { act } from "react";
import { createRoot } from "react-dom/client";
import { AppRouterContext, type AppRouterInstance } from "next/dist/shared/lib/app-router-context.shared-runtime";
import { PathnameContext } from "next/dist/shared/lib/hooks-client-context.shared-runtime";
import { installTestDOM, settleTestDOM } from "@/lib/assistant/test-dom";
import { ScreenError } from "./ScreenError";

function makeRouter(pushed: string[]): AppRouterInstance {
  return {
    back: () => {},
    forward: () => {},
    refresh: () => {},
    push: (href: string) => { pushed.push(href); },
    replace: () => {},
    prefetch: () => {},
    bfcacheId: "test",
  };
}

describe("ScreenError", () => {
  let restoreDOM: () => void;

  beforeEach(() => {
    restoreDOM = installTestDOM();
  });

  afterEach(() => {
    restoreDOM();
  });

  test("works with Next.js error boundary { error, reset } props without throwing", async () => {
    const pushed: string[] = [];
    let resets = 0;
    const target = document.createElement("div");
    document.body.append(target);
    const root = createRoot(target);
    try {
      await act(async () =>
        root.render(
          <PathnameContext.Provider value="/work">
            <AppRouterContext.Provider value={makeRouter(pushed)}>
              <ScreenError error={new Error("Boom")} reset={() => { resets++; }} />
            </AppRouterContext.Provider>
          </PathnameContext.Provider>,
        ),
      );
      await settleTestDOM();

      const buttons = [...target.querySelectorAll("button")];
      const tryAgain = buttons.find((b) => b.textContent === "Try again");
      const openChat = buttons.find((b) => b.textContent === "Open chat");
      assert.ok(tryAgain);
      assert.ok(openChat);

      await act(async () => tryAgain.click());
      assert.equal(resets, 1);

      await act(async () => openChat.click());
      assert.deepEqual(pushed, ["/"]);
      assert.equal(resets, 2);
    } finally {
      await act(async () => root.unmount());
      target.remove();
    }
  });

  test("falls back to retry prop when reset is not provided", async () => {
    const pushed: string[] = [];
    let retries = 0;
    const target = document.createElement("div");
    document.body.append(target);
    const root = createRoot(target);
    try {
      await act(async () =>
        root.render(
          <PathnameContext.Provider value="/more">
            <AppRouterContext.Provider value={makeRouter(pushed)}>
              <ScreenError error={new Error("Legacy")} retry={() => { retries++; }} />
            </AppRouterContext.Provider>
          </PathnameContext.Provider>,
        ),
      );
      await settleTestDOM();

      const tryAgain = [...target.querySelectorAll("button")].find((b) => b.textContent === "Try again");
      assert.ok(tryAgain);
      await act(async () => tryAgain.click());
      assert.equal(retries, 1);
    } finally {
      await act(async () => root.unmount());
      target.remove();
    }
  });
});
