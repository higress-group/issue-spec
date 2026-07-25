import { defineConfig, devices } from "@playwright/test";

export default defineConfig({
  testDir: ".",
  testMatch: "search.visual.e2e.ts",
  outputDir: "../../../test-results/search",
  fullyParallel: true,
  reporter: "list",
  use: { baseURL: "http://127.0.0.1:4175", trace: "retain-on-failure", screenshot: "only-on-failure" },
  projects: [
    { name: "search-desktop-1440", use: { ...devices["Desktop Chrome"], viewport: { width: 1440, height: 900 } } },
  ],
  webServer: { command: "npm run preview -- --host 127.0.0.1 --port 4175", url: "http://127.0.0.1:4175", reuseExistingServer: true },
});
