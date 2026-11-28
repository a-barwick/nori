import { expect, test } from "@playwright/test";

const appPort = process.env.NORI_E2E_APP_PORT ?? "4317";
const controlPort = process.env.NORI_E2E_CONTROL_PORT ?? "4318";
const authPort = process.env.NORI_E2E_AUTH_PORT ?? "9445";
const app = process.env.NORI_E2E_APP_URL ?? `http://127.0.0.1:${appPort}`;
const controlBase = process.env.NORI_E2E_CONTROL_URL ?? `http://127.0.0.1:${controlPort}`;
const authBase = process.env.NORI_E2E_AUTH_URL ?? `http://127.0.0.1:${authPort}`;
const controlURL = (path: string) => `${controlBase}${path}`;
function signInURL(account: string, path = "/") {
  return `${authBase}/sign-in?account=${account}&to=${encodeURIComponent(app + path)}`;
}

test("verifies authentication, queued turns, reconnect, approvals, account model selection, and downloads", async ({ page }) => {
  const signIn = (account: string) => page.goto(signInURL(account));
  await signIn("first");
  const capabilityResponse = page.waitForResponse(response => response.url().endsWith("/api/v1/capabilities") && response.ok());
  await page.reload();
  await capabilityResponse;
  const composer = page.getByRole("textbox", { name: "Message Nori" });
  await expect(composer).toBeEnabled();
  await composer.fill("First account draft");
  await page.getByRole("button", { name: "Send", exact: true }).click();
  await expect(page.getByText("Reply on default", { exact: true })).toBeVisible();
  const reconnected = page.waitForResponse(response => response.url().includes("/api/v1/events?after=") && response.ok());
  await composer.fill("Reconnect after acceptance");
  await page.getByRole("button", { name: "Send", exact: true }).click();
  await reconnected;
  await expect(page.getByText("Reply on default", { exact: true })).toHaveCount(2);

  await page.goto("/work");
  const allow = page.getByRole("button", { name: "Allow once", exact: true });
  await expect(allow).toBeVisible();
  const approved = page.waitForResponse(response => response.url().includes("/interactions/int_5/responses") && response.ok());
  await allow.click();
  await approved;
  await expect(allow).toHaveCount(0);

  await page.goto("/");
  await composer.fill("Send after access was revoked");
  await page.getByRole("button", { name: "Send", exact: true }).click();
  await expect(page.getByText("Reply on default", { exact: true })).toHaveCount(3);
  await composer.fill("Private unsent draft");

  await signIn("second");
  await expect(composer).toHaveValue("");
  await expect(page.getByText("First account draft", { exact: true })).toHaveCount(0);
  await composer.fill("Second account message");
  await page.getByRole("button", { name: "Send", exact: true }).click();
  await expect(page.getByText("Reply on default", { exact: true })).toBeVisible();

  const file = await page.evaluate(async () => {
    const response = await fetch("/api/v1/results/d3d34c1a-0ad7-4222-bff4-8cee91d892c5/file");
    return { status: response.status, disposition: response.headers.get("content-disposition"), text: await response.text() };
  });
  expect(file).toEqual({ status: 200, disposition: 'attachment; filename="fixture.txt"', text: "fixture download" });
});

test("Models offers recommendations after key setup and saves account choices", async ({ page }) => {
  let anthropicConfigured = false;
  let revision = 1;
  let selection = { chat_profile_id: "openai-sol", chat_reasoning: "low", task_profile_id: "openai-sol", task_reasoning: "medium" };
  const profiles = [
    { id: "openai-sol", version: "v1", label: "GPT-6.2 Sol", backend: "openai", model: "gpt-6.2-sol", lanes: ["chat", "task"], supported_reasoning: ["low", "medium", "high"], default_reasoning: "low", paid: true },
    { id: "anthropic-opus", version: "v1", label: "Claude Opus 5.5", backend: "anthropic", model: "claude-opus-5-5", lanes: ["chat", "task"], supported_reasoning: ["low", "high"], default_reasoning: "low", paid: true },
    { id: "anthropic-haiku", version: "v1", label: "Claude Haiku 5.5", backend: "anthropic", model: "claude-haiku-5-5", lanes: ["chat", "task"], supported_reasoning: ["low", "high"], default_reasoning: "low", paid: true },
  ];
  const recommended = { chat_profile_id: "anthropic-opus", chat_reasoning: "low", task_profile_id: "anthropic-haiku", task_reasoning: "low" };
  const writes: unknown[] = [];

  await page.route("**/api/v1/settings/model-keys**", async (route) => {
    if (route.request().method() === "PUT") {
      anthropicConfigured = true;
      await route.fulfill({ json: { provider: "anthropic", label: "Anthropic", configured: true, last4: "test" } });
      return;
    }
    await route.fulfill({ json: { byok_required: true, providers: [
      { provider: "openai", label: "OpenAI", configured: true },
      { provider: "anthropic", label: "Anthropic", configured: anthropicConfigured },
      { provider: "google", label: "Google", configured: false },
      { provider: "vercel_ai_gateway", label: "Vercel AI Gateway", configured: false },
      { provider: "openrouter", label: "OpenRouter", configured: false },
    ] } });
  });
  await page.route("**/api/v1/settings/models", async (route) => {
    if (route.request().method() === "PUT") {
      const body = route.request().postDataJSON();
      writes.push(body);
      expect(body.expected_revision).toBe(revision);
      selection = body.selection;
      revision++;
    }
    await route.fulfill({ json: {
      model_profiles: { profiles: profiles.filter((profile) => profile.backend === "openai" || anthropicConfigured) },
      selection,
      revision,
      recommendations: anthropicConfigured ? { anthropic: recommended } : {},
    } });
  });

  await page.goto(signInURL("first", "/settings/models"));
  await expect(page.getByRole("heading", { name: "Models", exact: true }).first()).toBeVisible();
  const anthropic = page.locator("li").filter({ hasText: "Anthropic" }).first();
  await anthropic.getByRole("button", { name: /Anthropic/ }).click();
  await anthropic.getByLabel("Anthropic API key").fill("sk-test");
  await anthropic.getByRole("button", { name: "Save", exact: true }).click();
  const dialog = page.getByRole("alertdialog");
  await expect(dialog).toBeVisible();
  await expect(dialog).toContainText("Claude Opus 5.5 · Low");
  await expect(dialog).toContainText("Claude Haiku 5.5 · Low");
  await dialog.getByRole("button", { name: "Keep current models" }).click();
  await expect(page.getByRole("button", { name: /Chat.*GPT-6\.2 Sol/ })).toBeVisible();
  expect(writes).toHaveLength(0);

  await page.getByRole("button", { name: /Chat.*GPT-6\.2 Sol/ }).click();
  await page.getByRole("button", { name: "Claude Opus 5.5" }).click();
  await expect.poll(() => writes.length).toBe(1);
  await page.getByRole("button", { name: "Models", exact: true }).click();
  await anthropic.getByRole("button", { name: /Anthropic/ }).click();
  await anthropic.getByLabel("Replace Anthropic API key").fill("sk-new-test");
  await anthropic.getByRole("button", { name: "Save", exact: true }).click();
  await dialog.getByRole("button", { name: "Use recommended models" }).click();
  await expect.poll(() => writes.length).toBe(2);
  await expect(page.getByRole("button", { name: /Work.*Claude Haiku 5\.5/ })).toBeVisible();
  await page.reload();
  await expect(page.getByRole("button", { name: /Chat.*Claude Opus 5\.5/ })).toBeVisible();
  await expect(page.getByRole("button", { name: /Work.*Claude Haiku 5\.5/ })).toBeVisible();
});

test("Work waits for pending approvals and offers a retry after a failed read", async ({ page, request }) => {
  expect((await request.post(controlURL("/reset-alternate"))).status()).toBe(204);
  let release!: () => void;
  let seen!: () => void;
  const waiting = new Promise<void>((resolve) => { seen = resolve; });
  const gate = new Promise<void>((resolve) => { release = resolve; });
  let attempt = 0;
  await page.route((url) => url.pathname === "/api/v1/interactions" && url.searchParams.get("status") === "pending", async (route) => {
    attempt++;
    if (attempt === 1) {
      seen();
      await gate;
      await route.fulfill({ status: 503, contentType: "application/json", body: JSON.stringify({ error: "Approval status unavailable" }) });
    } else {
      await route.continue();
    }
  });

  await page.goto(signInURL("second", "/work/tasks"));
  await waiting;
  await expect(page.getByRole("heading", { name: "Tasks", exact: true })).toBeVisible();
  await expect(page.getByText("All clear", { exact: true })).toHaveCount(0);
  release();
  await expect(page.getByRole("alert").filter({ hasText: "Approval status unavailable" })).toBeVisible();
  await expect(page.getByText("All clear", { exact: true })).toHaveCount(0);
  await page.getByRole("button", { name: "Try again" }).click();
  await expect(page.getByText("All clear", { exact: true })).toBeVisible();
});

test("Work reports a failed read while the pending approval read is still loading", async ({ page, request }) => {
  expect((await request.post(controlURL("/reset-alternate"))).status()).toBe(204);
  let release!: () => void;
  let seen!: () => void;
  const waiting = new Promise<void>((resolve) => { seen = resolve; });
  const gate = new Promise<void>((resolve) => { release = resolve; });
  await page.route((url) => url.pathname === "/api/v1/interactions" && url.searchParams.get("status") === "pending", async (route) => {
    seen();
    await gate;
    await route.continue();
  });
  let attempts = 0;
  await page.route("**/api/v1/work", async (route) => {
    attempts++;
    if (attempts === 1) {
      await route.fulfill({ status: 503, contentType: "application/json", body: JSON.stringify({ error: "Work status unavailable" }) });
    } else {
      await route.continue();
    }
  });

  await page.goto(signInURL("second", "/work/tasks"));
  await waiting;
  await expect(page.getByRole("alert").filter({ hasText: "Work status unavailable" })).toBeVisible();
  await expect(page.getByText("All clear", { exact: true })).toHaveCount(0);
  release();
  await page.getByRole("button", { name: "Try again" }).click();
  await expect(page.getByText("All clear", { exact: true })).toBeVisible();
});

test("Work reports a failed approval read while the Work read is still loading", async ({ page, request }) => {
  expect((await request.post(controlURL("/reset-alternate"))).status()).toBe(204);
  let release!: () => void;
  let seen!: () => void;
  const waiting = new Promise<void>((resolve) => { seen = resolve; });
  const gate = new Promise<void>((resolve) => { release = resolve; });
  await page.route("**/api/v1/work", async (route) => {
    seen();
    await gate;
    await route.continue();
  });
  let attempts = 0;
  await page.route((url) => url.pathname === "/api/v1/interactions" && url.searchParams.get("status") === "pending", async (route) => {
    attempts++;
    if (attempts === 1) {
      await route.fulfill({ status: 503, contentType: "application/json", body: JSON.stringify({ error: "Approval status unavailable" }) });
    } else {
      await route.continue();
    }
  });

  await page.goto(signInURL("second", "/work/tasks"));
  await waiting;
  await expect(page.getByRole("alert").filter({ hasText: "Approval status unavailable" })).toBeVisible();
  await expect(page.getByText("All clear", { exact: true })).toHaveCount(0);
  release();
  await page.getByRole("button", { name: "Try again" }).click();
  await expect(page.getByText("All clear", { exact: true })).toBeVisible();
});

test("Work reports unavailable capabilities and retries them after a successful empty snapshot", async ({ page, request }) => {
  expect((await request.post(controlURL("/reset-alternate"))).status()).toBe(204);
  let attempts = 0;
  await page.route("**/api/v1/capabilities", async (route) => {
    attempts++;
    if (attempts === 1) {
      await route.fulfill({ status: 503, contentType: "application/json", body: JSON.stringify({ error: "Capabilities unavailable" }) });
    } else {
      await route.continue();
    }
  });
  const snapshot = page.waitForResponse((response) => response.url().endsWith("/api/v1/snapshot") && response.ok());
  await page.goto(signInURL("second", "/work/tasks"));
  expect((await (await snapshot).json()).runs).toEqual([]);
  await expect(page.getByRole("alert").filter({ hasText: "Capabilities unavailable" })).toBeVisible();
  await expect(page.getByText("All clear", { exact: true })).toHaveCount(0);
  await page.getByRole("button", { name: "Try again" }).click();
  await expect(page.getByText("All clear", { exact: true })).toBeVisible();
});

test("a worker-created approval appears after a live task update", async ({ page, request }) => {
  expect((await request.post(controlURL("/reset-alternate"))).status()).toBe(204);
  const emptyPending = page.waitForResponse((response) => response.url().endsWith("/api/v1/interactions?status=pending") && response.ok());
  const eventStream = page.waitForResponse((response) => response.url().includes("/api/v1/events?after=") && response.ok());
  await page.goto(signInURL("second", "/work"));
  expect((await (await emptyPending).json()).items).toEqual([]);
  await eventStream;
  await expect(page.getByRole("heading", { name: "Workspace", exact: true })).toBeVisible();
  await expect(page.getByRole("button", { name: "Allow once", exact: true })).toHaveCount(0);

  const created = await request.post(controlURL("/worker-approval"));
  expect(created.status()).toBe(204);
  await expect(page.getByRole("button", { name: "Allow once", exact: true })).toBeVisible({ timeout: 5_000 });
});

test.describe("routine scheduling", () => {
  test.use({ timezoneId: "UTC" });

  test("preview and save end on the DST transition day in the routine timezone", async ({ page }) => {
    await page.goto(signInURL("first", "/routines/new"));
    await page.getByLabel("Name", { exact: true }).fill("Daily check");
    await page.getByLabel("How often").selectOption("daily");
    await page.getByLabel("Starting").fill("2026-03-07");
    await page.getByLabel("Time zone").fill("America/Los_Angeles");
    await page.getByLabel("Stop after (optional)").fill("2026-03-08");

    const previewResponse = page.waitForResponse(response => response.url().endsWith("/api/v1/automations/preview") && response.ok());
    await page.getByLabel("What Nori does each time").fill("Review the test schedule");
    const preview = (await previewResponse).request().postDataJSON();
    expect(preview.trigger.timezone).toBe("America/Los_Angeles");
    expect(preview.end_at).toBe("2026-03-09T06:59:59.999Z");
    await expect(page.getByRole("heading", { name: "Next runs" })).toBeVisible();

    const saved = page.waitForResponse(response => response.url().endsWith("/api/v1/automations") && response.request().method() === "POST" && response.ok());
    await page.getByRole("button", { name: "Create routine" }).click();
    expect((await saved).request().postDataJSON().value).toEqual(preview);
  });
});

test("sign out closes other tabs and sign in restores the workspace", async ({ page, context }) => {
  await page.goto(signInURL("first", "/settings/account"));
  const secondTab = await context.newPage();
  await secondTab.goto(`${app}/more`);
  await page.getByRole("button", { name: "Sign out" }).click();
  await expect(page).toHaveURL(/\/signed-out$/);
  await expect(secondTab).toHaveURL(/\/signed-out$/);
  await expect(page.getByRole("button", { name: "Sign in with Google" })).toBeVisible();
  await expect(secondTab.getByRole("button", { name: "Sign in with Google" })).toBeVisible();
  expect(await page.evaluate(async () => (await fetch("/api/v1/preferences")).status)).toBe(403);

  await page.getByRole("button", { name: "Sign in with Google" }).click();
  await page.goto(signInURL("first", "/more"));
  await expect(page.getByRole("link", { name: "Account" })).toBeVisible();
  await secondTab.close();
});

test("an interrupted reset can be safely resumed after reload", async ({ page, request }) => {
  expect((await request.post(controlURL("/prepare-reset-alternate"))).status()).toBe(204);
  await page.goto(signInURL("second", "/settings/reset"));
  expect((await request.post(controlURL("/interrupt-reset-alternate"))).status()).toBe(204);
  let delayFirstAttempt = true;
  let releaseFirstAttempt!: () => void;
  let firstAttemptStarted!: () => void;
  const holdFirstAttempt = new Promise<void>((resolve) => { releaseFirstAttempt = resolve; });
  const firstAttemptObserved = new Promise<void>((resolve) => { firstAttemptStarted = resolve; });
  await page.route("**/api/v1/settings/reset", async (route) => {
    if (delayFirstAttempt) {
      delayFirstAttempt = false;
      firstAttemptStarted();
      await holdFirstAttempt;
    }
    await route.continue();
  });
  await page.getByLabel("Type RESET to confirm").fill("RESET");
  const firstAttempt = page.waitForResponse((response) => response.url().endsWith("/api/v1/settings/reset") && response.request().method() === "POST");
  await page.getByRole("button", { name: "Reset data" }).click();
  await firstAttemptObserved;
  await expect(page.getByRole("status").filter({ hasText: "Resetting data…" })).toBeVisible();
  releaseFirstAttempt();
  expect((await firstAttempt).status()).toBe(502);
  await expect(page.getByRole("heading", { name: "Finish your data reset" })).toBeVisible();
  await expect(page.getByLabel("OpenAI API key")).toHaveCount(0);

  await page.reload();
  await expect(page.getByRole("heading", { name: "Finish your data reset" })).toBeVisible();
  expect((await request.post(controlURL("/interrupt-reset-alternate"))).status()).toBe(204);
  await page.getByRole("button", { name: "Finish reset" }).click();
  await expect(page.getByText("Nori couldn’t finish the reset. Try again.")).toBeVisible();
  const resume = page.waitForResponse((response) => response.url().endsWith("/api/v1/settings/reset") && response.request().method() === "POST");
  await page.getByRole("button", { name: "Finish reset" }).click();
  const response = await resume;
  expect(response.status()).toBe(204);
  expect(response.request().postDataJSON()).toEqual({ confirmation: "RESET", resume_pending: true });
  await expect(page.getByRole("heading", { name: "Data reset complete" })).toBeVisible();
  await expect(page.getByText(/saved API keys and model choices were cleared/)).toBeVisible();
  await page.reload();
  await expect(page.getByRole("heading", { name: "Data reset complete" })).toBeVisible();
  await page.getByRole("button", { name: "Continue" }).click();
  await expect(page.getByRole("button", { name: "Get started" })).toBeVisible();
});

test("a key save blocked by reset enters recovery and a stale resume preserves newer data", async ({ page, request }) => {
  expect((await request.post(controlURL("/prepare-reset-alternate"))).status()).toBe(204);
  await page.goto(signInURL("second", "/settings/models"));
  const openai = page.locator("li").filter({ hasText: "OpenAI" }).first();
  await openai.getByRole("button", { name: /OpenAI/ }).click();
  await openai.getByLabel("OpenAI API key").fill("sk-test");
  expect((await request.post(controlURL("/pending-reset-alternate"))).status()).toBe(204);
  await openai.getByRole("button", { name: "Save", exact: true }).click();
  await expect(page.getByRole("heading", { name: "Finish your data reset" })).toBeVisible();
  await expect(page.getByLabel("OpenAI API key")).toHaveCount(0);

  expect((await request.post(controlURL("/finish-reset-alternate-with-new-data"))).status()).toBe(204);
  const staleResume = page.waitForResponse((response) => response.url().endsWith("/api/v1/settings/reset") && response.request().method() === "POST");
  await page.getByRole("button", { name: "Finish reset" }).click();
  const response = await staleResume;
  expect(response.status()).toBe(409);
  expect(response.request().postDataJSON()).toEqual({ confirmation: "RESET", resume_pending: true });
  await expect(page.getByRole("heading", { name: "Models", exact: true })).toBeVisible();
  await expect(page.getByRole("heading", { name: "Data reset complete" })).toHaveCount(0);
  const after = await page.evaluate(async () => ({
    keys: await (await fetch("/api/v1/settings/model-keys")).json(),
    models: await (await fetch("/api/v1/settings/models")).json(),
  }));
  expect(after.keys.providers.find((provider: { provider: string }) => provider.provider === "google").configured).toBe(true);
  expect(after.models.selection.chat_profile_id).toBe("google-neon");
});

test("reset clears saved model keys and choices and opens onboarding in both tabs", async ({ page, context, request }) => {
  expect((await request.post(controlURL("/seed-models"))).status()).toBe(204);
  await page.goto(signInURL("first", "/more"));
  const before = await page.evaluate(async () => ({
    keys: await (await fetch("/api/v1/settings/model-keys")).json(),
    models: await (await fetch("/api/v1/settings/models")).json(),
  }));
  expect(before.keys.providers).toHaveLength(5);
  expect(before.keys.providers.every((provider: { configured: boolean }) => provider.configured)).toBe(true);
  expect(before.models.selection.chat_profile_id).toBe("anthropic-opus");
  const secondTab = await context.newPage();
  await secondTab.goto(`${app}/more`);
  await expect(secondTab.getByText("Settings", { exact: true })).toBeVisible();
  await page.getByRole("link", { name: "Account" }).click();
  await page.getByRole("link", { name: "Reset data" }).click();
  await expect(page.getByText(/saved API keys, and model choices/)).toBeVisible();
  const confirm = page.getByLabel("Type RESET to confirm");
  const reset = page.getByRole("button", { name: "Reset data" });
  await expect(reset).toBeDisabled();
  await confirm.fill("reset");
  await expect(reset).toBeDisabled();
  await confirm.fill("RESET");
  await reset.click();
  await expect(page.getByRole("heading", { name: "Data reset complete" })).toBeVisible();
  await expect(secondTab.getByRole("heading", { name: "Data reset complete" })).toBeVisible();
  await page.getByRole("button", { name: "Continue" }).click();
  await secondTab.getByRole("button", { name: "Continue" }).click();
  await expect(page.getByRole("button", { name: "Get started" })).toBeVisible();
  await expect(secondTab.getByRole("button", { name: "Get started" })).toBeVisible();
  await expect(page.getByLabel("Step 1 of 4")).toBeVisible();
  await expect(secondTab.getByLabel("Step 1 of 4")).toBeVisible();
  for (const tab of [page, secondTab]) {
    const after = await tab.evaluate(async () => ({
      keys: await (await fetch("/api/v1/settings/model-keys")).json(),
      models: await (await fetch("/api/v1/settings/models")).json(),
    }));
    expect(after.keys.providers).toHaveLength(5);
    expect(after.keys.providers.every((provider: { configured: boolean }) => !provider.configured)).toBe(true);
    expect(after.models.selection).toEqual({});
  }
  await page.getByRole("button", { name: "Get started" }).click();
  await page.getByRole("button", { name: "Continue" }).click();
  await expect(page.getByLabel("Step 3 of 4")).toBeVisible();
  for (const provider of ["openai", "anthropic", "google", "vercel_ai_gateway", "openrouter"]) {
    await page.getByLabel("Model provider").selectOption(provider);
    await expect(page.getByLabel("API key")).toBeVisible();
    await expect(page.getByText("Configured", { exact: true })).toHaveCount(0);
  }
  await secondTab.close();
});
