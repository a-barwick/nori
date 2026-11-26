import { expect, test } from "@playwright/test";

const app = "http://127.0.0.1:4317";
const first = "462cef1f-74a0-4706-94c7-3e624ff5cd43";
function signInURL(account: string, path = "/") {
  return `http://127.0.0.1:9445/sign-in?account=${account}&to=${encodeURIComponent(app + path)}`;
}

test("verifies authentication, queued turns, reconnect, approvals, model access, and downloads", async ({ page }) => {
  const signIn = (account: string) => page.goto(signInURL(account));
  await signIn("first");
  await page.evaluate((id) => localStorage.setItem("nori-diagnostics-model-selection-v1:" + id, JSON.stringify({ chat_profile_id: "browser-chat" })), first);
  const capabilityResponse = page.waitForResponse(response => response.url().endsWith("/api/v1/capabilities") && response.ok());
  await page.reload();
  await capabilityResponse;
  const composer = page.getByRole("textbox", { name: "Message Nori" });
  await expect(composer).toBeEnabled();
  await composer.fill("First account draft");
  await page.getByRole("button", { name: "Send", exact: true }).click();
  await expect(page.getByText("Reply on browser-chat", { exact: true })).toBeVisible();
  const reconnected = page.waitForResponse(response => response.url().includes("/api/v1/events?after=") && response.ok());
  await composer.fill("Reconnect after acceptance");
  await page.getByRole("button", { name: "Send", exact: true }).click();
  await reconnected;
  await expect(page.getByText("Reply on browser-chat", { exact: true })).toHaveCount(2);

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
  await expect(page.getByText("Reply on default", { exact: true })).toBeVisible();
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
