import assert from "node:assert/strict";
import { afterEach, beforeEach, test } from "node:test";
import { act } from "react";
import { createRoot } from "react-dom/client";
import { ResourceCacheContext, createResourceCache } from "@/lib/assistant/useResource";
import { installTestDOM } from "@/lib/assistant/test-dom";
import type { DesktopBridge } from "@/lib/desktop";
import { DesktopHomeSharing } from "./DesktopHomeSharing";

let restoreDOM: () => void;
beforeEach(() => { restoreDOM = installTestDOM(); });
afterEach(() => { restoreDOM(); });

test("a pending disconnect blocks enabling sharing", async () => {
  const desktop = { disconnectHome: async () => ({ revocationPending: true }) } as DesktopBridge;
  const target = document.createElement("div");
  document.body.append(target);
  const root = createRoot(target);
  try {
    await act(async () => root.render(
      <ResourceCacheContext.Provider value={createResourceCache()}>
        <DesktopHomeSharing desktop={desktop} status={{ paired: false, background: "off", revocationPending: true }}
          onStatus={() => {}} devices={[]} available scope="test-account" />
      </ResourceCacheContext.Provider>,
    ));
    const button = [...target.querySelectorAll("button")].find((item) => item.textContent === "Set up");
    assert.equal(button?.disabled, true);
    assert.match(target.textContent ?? "", /finish disconnecting when the network is available/);
  } finally {
    await act(async () => root.unmount());
    target.remove();
  }
});
