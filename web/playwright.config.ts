import { defineConfig, devices } from "@playwright/test";

export default defineConfig({
  testDir: "./e2e",
  workers: 1,
  use: { ...devices["Desktop Chrome"], baseURL: "http://127.0.0.1:4317" },
  webServer: {
    command: "node e2e/fixture.mjs",
    url: "http://127.0.0.1:4317/api/health",
    timeout: 45_000,
  },
});
