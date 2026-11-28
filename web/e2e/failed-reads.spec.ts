import { expect, test, type Page } from "@playwright/test";

const app = process.env.NORI_E2E_APP_URL ?? `http://127.0.0.1:${process.env.NORI_E2E_APP_PORT ?? "4317"}`;
const auth = process.env.NORI_E2E_AUTH_URL ?? `http://127.0.0.1:${process.env.NORI_E2E_AUTH_PORT ?? "9445"}`;
const api = (resource: string) => `**/api/v1/${resource}`;
const date = "2026-11-27T11:00:00Z";
const capabilities = {
  contract: "nori-chat-2",
  service: "nori",
  conversationModes: ["assistant"],
  assistant_features: { work: true, interactions: true, mcp: true, memory: true, permissions: true, automations: true, library: true },
};
const mcp = {
  id: "mcp_saved", revision: 1, source: { kind: "user" }, name: "Team workspace",
  transport: "http", endpoint: "https://example.com/mcp", auth: "none", enabled: true, observed: { status: "connected" },
};
const fact = {
  id: "fact_saved", revision: 1, text: "The garden project starts in May.", section: "Projects",
  scope: "user", origin: "user", created_at: date,
};
const permission = {
  id: "rule_saved", revision: 1, effect: "allow", summary: "Send the weekly status email",
  tool_ids: ["mail.send"], connection_ids: ["conn_saved"], resource_ids: [], action_classes: ["sensitive_send"],
};
const automation = {
  id: "auto_saved", revision: 1, title: "Weekly status email", instruction: "Send the weekly status email.",
  status: "enabled", trigger: { kind: "recurring", local_start: "2026-11-27T11:00", timezone: "UTC", rrule: "FREQ=WEEKLY;BYDAY=FR" },
  next_at: date, created_at: date, updated_at: date, connection_ids: [], budget_usd: 0.5,
  max_active_runs: 1, missed_run: "latest", execution_window_seconds: 3600, destination_ids: [], authority_rule_ids: [],
};
const artifact = {
  id: "artifact_saved", root_id: "artifact_saved", version: 1, revision: 1, kind: "document", title: "Trip checklist",
  preview_text: "A list for the trip.", provenance: { content: "generated" }, created_at: date,
};

function openScreen(page: Page, path: string) {
  return page.goto(`${auth}/sign-in?account=first&to=${encodeURIComponent(app + path)}`);
}

function refreshReads(page: Page) {
  return page.evaluate(() => window.dispatchEvent(new Event("online")));
}

test.beforeEach(async ({ page }) => {
  await page.route(api("preferences"), (route) => route.fulfill({ json: {
    revision: 1, timezone: "UTC", working_hours: [], default_destination_ids: [], account_defaults: {},
    budgets: { task_usd: 5 }, onboarding_completed_at: date,
  } }));
});

test("Settings only marks confirmed unavailable features as Not yet", async ({ page }) => {
  const initialRead = Promise.withResolvers<void>();
  let available = false;
  await page.route(api("capabilities"), async (route) => {
    await initialRead.promise;
    if (!available) return route.fulfill({ status: 503, json: { error: "Could not check features" } });
    return route.fulfill({ json: capabilities });
  });

  await openScreen(page, "/settings");
  const permissions = page.getByRole("link", { name: /Permissions/ });
  const notifications = page.getByRole("link", { name: /Notifications/ });
  const notYet = page.getByText("Not yet", { exact: true });
  await expect(permissions).toBeVisible();
  await expect(notYet).toHaveCount(0);
  initialRead.resolve();
  const failure = page.getByRole("alert").filter({ hasText: "Could not check features" });
  await expect(failure).toBeVisible();
  await expect(notYet).toHaveCount(0);
  available = true;
  await failure.getByRole("button", { name: "Try again" }).click();
  await expect(failure).toHaveCount(0);
  await expect(notifications).toContainText("Not yet");
  await expect(permissions).toHaveText("Permissions");
});

for (const { title, path, resource, item, savedText } of [
  { title: "Apps", path: "/apps", resource: "mcp", item: mcp, savedText: mcp.name },
  { title: "Memory", path: "/memory", resource: "memory", item: fact, savedText: fact.section },
  { title: "Artifacts", path: "/library", resource: "library", item: artifact, savedText: artifact.title },
]) {
  test(`${title} retries unavailable capabilities and retains saved content during refresh`, async ({ page }) => {
    let available = false;
    let reads = 0;
    await page.route(api("capabilities"), (route) =>
      available
        ? route.fulfill({ json: capabilities })
        : route.fulfill({ status: 503, json: { error: "Could not check features" } }),
    );
    await page.route(api(resource), (route) => {
      reads++;
      return route.fulfill({ json: { items: [item] } });
    });

    await openScreen(page, path);
    await expect(page.getByRole("heading", { name: title, exact: true })).toBeVisible();
    await expect(page.getByRole("alert").filter({ hasText: "Could not check features" })).toBeVisible();
    await expect(page.getByText(/isn’t available on this Nori yet|Nothing saved yet/)).toHaveCount(0);
    expect(reads).toBe(0);
    available = true;
    await page.getByRole("button", { name: "Try again" }).click();
    await expect(page.getByText(savedText, { exact: true })).toBeVisible();

    available = false;
    const failedRefresh = page.waitForResponse((response) =>
      response.url().endsWith("/api/v1/capabilities") && response.status() === 503,
    );
    await refreshReads(page);
    await failedRefresh;
    await expect(page.getByText(savedText, { exact: true })).toBeVisible();
    await expect(page.getByText(/isn’t available on this Nori yet|Nothing saved yet/)).toHaveCount(0);
  });
}

for (const { title, path, resource, item, savedText, empty } of [
  { title: "Permissions", path: "/settings/permissions", resource: "permissions", item: permission, savedText: permission.summary, empty: "Nori asks before making changes or sending messages." },
  { title: "Automations", path: "/routines", resource: "automations", item: automation, savedText: automation.title, empty: "No automations yet" },
  { title: "Artifacts", path: "/library", resource: "library", item: artifact, savedText: artifact.title, empty: "Nothing saved yet" },
  { title: "Memory", path: "/memory", resource: "memory", item: fact, savedText: fact.section, empty: "Nori will remember helpful details as you chat." },
  { title: "Apps", path: "/apps", resource: "mcp", item: mcp, savedText: mcp.name, empty: "No custom apps yet." },
]) {
  test(`${title} shows a retry when its saved list fails to load`, async ({ page }) => {
    let available = false;
    const message = `${title} read failed`;
    const failure = page.getByRole("alert").filter({ hasText: message });
    const emptyState = page.getByText(empty, { exact: true });
    const row = page.getByText(savedText, { exact: true });
    await page.route(api("capabilities"), (route) => route.fulfill({ json: capabilities }));
    await page.route(api(resource), (route) =>
      available
        ? route.fulfill({ json: { items: [item] } })
        : route.fulfill({ status: 503, json: { error: message } }),
    );

    await openScreen(page, path);
    await expect(failure).toBeVisible();
    await expect(emptyState).toHaveCount(0);
    await expect(row).toHaveCount(0);
    available = true;
    await failure.getByRole("button", { name: "Try again" }).click();
    await expect(row).toBeVisible();
    await expect(failure).toHaveCount(0);
    if (resource === "permissions") await expect(page.getByRole("heading", { name: "Allowed" })).toBeVisible();

    available = false;
    await refreshReads(page);
    await expect(failure).toBeVisible();
    await expect(row).toBeVisible();
    await expect(emptyState).toHaveCount(0);
    available = true;
    await failure.getByRole("button", { name: "Try again" }).click();
    await expect(failure).toHaveCount(0);
    await expect(row).toBeVisible();
  });
}
