import { configDefaults, defineConfig } from "vitest/config";
import react from "@vitejs/plugin-react";

export default defineConfig({
  plugins: [react()],
  build: {
    manifest: true,
    sourcemap: false,
    target: "es2022",
  },
  test: {
    environment: "jsdom",
    globals: true,
    setupFiles: "./tests/setup.ts",
    css: true,
    restoreMocks: true,
    environmentOptions: { jsdom: { url: "http://localhost/" } },
    exclude: [...configDefaults.exclude, "tests/e2e/**"],
  },
});
