import { defineConfig, devices } from "@playwright/test";

const appPort = process.env.NORI_E2E_APP_PORT ?? "4317";
const appURL = process.env.NORI_E2E_APP_URL ?? `http://127.0.0.1:${appPort}`;

export default defineConfig({
  testDir: "./e2e",
  workers: 1,
  use: { ...devices["Desktop Chrome"], baseURL: appURL },
  webServer: {
    command: "node e2e/fixture.mjs",
    url: `${appURL}/api/health`,
    timeout: 45_000,
  },
});
