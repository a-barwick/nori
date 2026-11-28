import { expect, test, type Page } from "@playwright/test";

const app = process.env.NORI_E2E_APP_URL ?? `http://127.0.0.1:${process.env.NORI_E2E_APP_PORT ?? "4317"}`;
const auth = process.env.NORI_E2E_AUTH_URL ?? `http://127.0.0.1:${process.env.NORI_E2E_AUTH_PORT ?? "9445"}`;
const control = process.env.NORI_E2E_CONTROL_URL ?? `http://127.0.0.1:${process.env.NORI_E2E_CONTROL_PORT ?? "4318"}`;

test("hosted navigation reuses verified identity resolutions for two signed-in clients", async ({ page, browser, request }) => {
  const counts = async (): Promise<Record<string, number>> => {
    const response = await request.get(`${control}/identity-resolutions`);
    expect(response.ok()).toBe(true);
    return response.json();
  };
  const before = await counts();
  const other = await browser.newPage();
  async function traverse(client: Page, account: string) {
    const signIn = `${auth}/sign-in?account=${account}&to=${encodeURIComponent(`${app}/more`)}`;
    expect((await client.goto(signIn))?.status()).toBe(200);
    for (const path of ["/work", "/library", "/settings/permissions", "/memory", "/apps", "/more"]) {
      expect((await client.goto(app + path))?.status()).toBe(200);
      const capabilities = await client.evaluate(async () => (await fetch("/api/v1/capabilities")).status);
      expect(capabilities).toBe(200);
    }
  }
  try {
    await Promise.all([traverse(page, "first"), traverse(other, "second")]);
    const after = await counts();
    for (const email of ["frontend@example.com", "second@example.com"]) {
      expect(after[email]).toBeGreaterThan(0);
      expect(after[email] - (before[email] ?? 0)).toBeLessThanOrEqual(2);
    }
  } finally {
    await other.close();
  }
});
