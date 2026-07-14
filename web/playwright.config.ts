import { defineConfig } from "@playwright/test";

export default defineConfig({
  testDir: "./tests/e2e",
  fullyParallel: false,
  timeout: 30_000,
  expect: { timeout: 5000 },
  outputDir: "/tmp/agent-bridge-playwright",
  reporter: "line",
  use: {
    browserName: "chromium",
    headless: true,
    screenshot: "only-on-failure",
    trace: "retain-on-failure",
  },
  webServer: [
    {
      command: "npm run dev:local -- --host 127.0.0.1",
      url: "http://127.0.0.1:4202",
      reuseExistingServer: true,
      timeout: 30_000,
    },
    {
      command: "npm run dev:remote -- --host 127.0.0.1",
      url: "http://127.0.0.1:4201",
      reuseExistingServer: true,
      timeout: 30_000,
    },
  ],
});
