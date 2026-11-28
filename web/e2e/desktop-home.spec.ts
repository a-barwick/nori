import { expect, test } from "@playwright/test";

const app = process.env.NORI_E2E_APP_URL ?? `http://127.0.0.1:${process.env.NORI_E2E_APP_PORT ?? "4317"}`;
const auth = process.env.NORI_E2E_AUTH_URL ?? `http://127.0.0.1:${process.env.NORI_E2E_AUTH_PORT ?? "9445"}`;
const location = (path: string) => process.env.NORI_MAC_TEST_DIRECT === "1"
  ? path
  : `${auth}/sign-in?account=first&to=${encodeURIComponent(app + path)}`;

test("Mac setup asks for background approval before pairing and changes only the selected model route", async ({ page }) => {
  const writes: unknown[] = [];
  await page.addInitScript(() => {
    type State = { paired: boolean; background: "off" | "enabled"; calls: string[] };
    const read = (): State => JSON.parse(sessionStorage.getItem("mac-test") ?? '{"paired":false,"background":"off","calls":[]}');
    const update = (change: Partial<State>, call?: string) => {
      const current = read();
      const next = { ...current, ...change, calls: call ? [...current.calls, call] : current.calls };
      sessionStorage.setItem("mac-test", JSON.stringify(next));
      return next;
    };
    window.noriDesktop = {
      homeStatus: async () => read(),
      ollamaModels: async () => ({ ollama_available: true, models: [{ name: "qwen:latest", capabilities: ["completion", "tools"] }] }),
      registerBackground: async () => update({ background: "enabled" }, "register"),
      openBackgroundSettings: async () => {},
      beginPairing: async () => {
        if (read().background !== "enabled") throw new Error("Background approval required.");
        update({}, "begin");
        return { user_code: "ABCD-EFGH" };
      },
      finishPairing: async () => { update({ paired: true }, "finish"); },
      cancelPairing: async () => { update({}, "cancel"); },
      disconnectHome: async () => { update({ paired: false, background: "off" }, "disconnect"); return { revocationPending: false }; },
      signOut: async () => { update({}, "signout"); },
      openInBrowser: async () => { update({}, "browser"); },
    };
  });
  await page.route("**/api/v1/settings/home-connectors/pairing/approve", async (route) => {
    expect(route.request().postDataJSON()).toEqual({ user_code: "ABCD-EFGH" });
    await route.fulfill({ status: 204 });
  });
  await page.route("**/api/v1/preferences", async (route) => {
    await route.fulfill({ json: { revision: 1, timezone: "UTC", working_hours: [], default_destination_ids: [], account_defaults: {}, budgets: { task_usd: 5 }, onboarding_completed_at: "2026-01-01T00:00:00Z" } });
  });
  await page.route("**/api/v1/settings/home-connectors", async (route) => {
    await route.fulfill({ json: { enabled: true, devices: [{ id: "device", name: "Mac", status: "online", models: [] }] } });
  });
  const profile = {
    id: "home-model", version: "1", label: "qwen:latest · Mac", backend: "home", model: "qwen:latest",
    home_device_id: "device", lanes: ["chat", "task"], paid: false,
  };
  let selection = { chat_profile_id: "old-chat", chat_reasoning: "high", task_profile_id: "old-work", task_reasoning: "low" };
  let revision = 7;
  await page.route("**/api/v1/settings/models", async (route) => {
    if (route.request().method() === "PUT") {
      const body = route.request().postDataJSON();
      writes.push(body);
      selection = body.selection;
      revision++;
    }
    await route.fulfill({ json: { model_profiles: { profiles: [profile] }, selection, revision, recommendations: {} } });
  });
  await page.goto(location("/settings/models"));
  await page.getByRole("button", { name: "Set up", exact: true }).click();
  const dialog = page.getByRole("dialog");
  await expect(dialog.getByRole("option", { name: "qwen:latest" })).toHaveCount(1);
  await dialog.getByRole("button", { name: "Continue", exact: true }).click();
  await expect(dialog.getByText("Allow Nori to run in the background", { exact: false })).toBeVisible();
  await page.reload();
  await page.getByRole("button", { name: "Continue setup" }).click();
  await expect(dialog.getByText("Allow Nori to run in the background", { exact: false })).toBeVisible();
  await dialog.getByRole("button", { name: "Allow background access" }).click();
  await dialog.getByRole("button", { name: "Connect this Mac" }).click();
  await expect(dialog.getByRole("checkbox", { name: "Use for Chat" })).toBeEnabled();
  await dialog.getByRole("checkbox", { name: "Use for Chat" }).check();
  await dialog.getByRole("button", { name: "Done", exact: true }).click();
  await expect(dialog).toHaveCount(0);
  expect(writes).toEqual([{
    expected_revision: 7,
    selection: { chat_profile_id: "home-model", task_profile_id: "old-work", task_reasoning: "low" },
  }]);
  expect(await page.evaluate(() => JSON.parse(sessionStorage.getItem("mac-test") ?? "{}").calls))
    .toEqual(["register", "begin", "finish"]);
});

test("desktop connection opens Nori in a browser and the browser starts a fresh sign-in", async ({ page }) => {
  let attempts = 0;
  let oldURLVisited = false;
  await page.addInitScript(() => {
    if (new URLSearchParams(window.location.search).has("nori_desktop_continue")) return;
    window.noriDesktop = {
      homeStatus: async () => ({ paired: false, background: "off" }),
      ollamaModels: async () => ({ ollama_available: false }),
      registerBackground: async () => ({ paired: false, background: "off" }),
      openBackgroundSettings: async () => {},
      beginPairing: async () => ({ user_code: "TEST-1234" }),
      finishPairing: async () => {},
      cancelPairing: async () => {},
      disconnectHome: async () => ({ revocationPending: false }),
      signOut: async () => {},
      openInBrowser: async () => { sessionStorage.setItem("browser-opened", "1"); },
    };
  });
  await page.route("**/api/v1/preferences", async (route) => {
    await route.fulfill({ json: { revision: 1, timezone: "UTC", working_hours: [], default_destination_ids: [], account_defaults: {}, budgets: { task_usd: 5 }, onboarding_completed_at: "2026-01-01T00:00:00Z" } });
  });
  await page.route("**/api/v1/capabilities", async (route) => {
    await route.fulfill({ json: { assistant_features: { work: true, interactions: true }, features: {} } });
  });
  await page.route("**/api/v1/work", async (route) => { await route.fulfill({ json: { items: [] } }); });
  await page.route((url) => url.pathname === "/api/v1/interactions" && url.searchParams.get("status") === "pending", async (route) => {
    await route.fulfill({ json: { items: [{
      id: "signin", revision: 1, kind: "sign_in", status: "pending", prompt: "Connect your account",
      payload: { integration_id: "google", connection_id: "connection", auth: "oauth", authorize_url: "https://old.example/authorize" },
    }] } });
  });
  await page.route("**/api/v1/integrations/google", async (route) => {
    await route.fulfill({ json: { id: "google", title: "Google", capabilities: [] } });
  });
  await page.route("**/api/v1/connections/connection", async (route) => {
    await route.fulfill({ json: { id: "connection", revision: 3, integration_id: "google", title: "Google", status: "needs_attention", health: {}, account: { label: "User" } } });
  });
  await page.route("**/api/v1/connections/connection/actions", async (route) => {
    attempts++;
    expect(route.request().postDataJSON().value.action).toBe("repair");
    await route.fulfill({ json: { auth: { authorize_url: "https://fresh.example/authorize" } } });
  });
  await page.route("https://old.example/**", async (route) => { oldURLVisited = true; await route.abort(); });
  await page.route("https://fresh.example/**", async (route) => { await route.fulfill({ status: 200, contentType: "text/html", body: "Fresh sign-in" }); });
  await page.goto(location("/work"));
  await page.getByRole("button", { name: "Continue in browser" }).click();
  expect(await page.evaluate(() => sessionStorage.getItem("browser-opened"))).toBe("1");
  expect(attempts).toBe(0);
  await page.goto(location("/work?nori_desktop_continue=1"));
  await page.getByRole("button", { name: "Sign in", exact: true }).click();
  await expect(page.getByText("Fresh sign-in")).toBeVisible();
  expect(attempts).toBe(1);
  expect(oldURLVisited).toBe(false);
});
