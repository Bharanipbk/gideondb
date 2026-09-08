import { defineConfig, devices } from "@playwright/test";

export default defineConfig({
  testDir: "./tests",
  outputDir: "../../../../.dashboard-test-results",
  fullyParallel: false,
  retries: process.env.CI ? 2 : 0,
  reporter: process.env.CI ? "github" : "line",
  use: {
    baseURL: "http://127.0.0.1:16334",
    trace: "retain-on-failure",
  },
  webServer: {
    command: "../../../../scripts/run-dashboard-e2e-server.sh",
    url: "http://127.0.0.1:16334/v1/health",
    timeout: 120_000,
    reuseExistingServer: false,
  },
  projects: [
    { name: "desktop-chromium", use: { ...devices["Desktop Chrome"] } },
    { name: "mobile-chromium", use: { ...devices["Pixel 7"] } },
  ],
});
