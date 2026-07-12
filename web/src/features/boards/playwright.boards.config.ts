import { defineConfig, devices } from "@playwright/test";

export default defineConfig({
  testDir: ".",
  testMatch: "boards.visual.e2e.ts",
  outputDir: "../../../test-results/boards",
  fullyParallel: true,
  reporter: "list",
  use: { baseURL: "http://127.0.0.1:4174", trace: "retain-on-failure", screenshot: "only-on-failure" },
  projects: [
    { name: "boards-desktop-1440", use: { ...devices["Desktop Chrome"], viewport: { width: 1440, height: 900 } } },
    { name: "boards-tablet-1024", use: { ...devices["Desktop Chrome"], viewport: { width: 1024, height: 820 } } },
    { name: "boards-mobile-390", use: { ...devices["Desktop Chrome"], viewport: { width: 390, height: 844 }, hasTouch: true, isMobile: true } },
    { name: "boards-reflow-200-percent", use: { ...devices["Desktop Chrome"], viewport: { width: 720, height: 900 } } },
  ],
  webServer: { command: "npm run preview -- --host 127.0.0.1 --port 4174", url: "http://127.0.0.1:4174", reuseExistingServer: true },
});
