import "@testing-library/jest-dom/vitest";
import { cleanup } from "@testing-library/react";
import { afterAll, afterEach, beforeAll } from "vitest";
import { server } from "./server";
import i18n from "../src/i18n/i18n";

Object.defineProperty(window, "matchMedia", {
  writable: true,
  value: (query: string) => ({ matches: false, media: query, onchange: null, addListener: () => {}, removeListener: () => {}, addEventListener: () => {}, removeEventListener: () => {}, dispatchEvent: () => false }),
});

beforeAll(() => server.listen({ onUnhandledRequest: "error" }));
afterEach(async () => {
  cleanup();
  server.resetHandlers();
  document.cookie = "issue_spec_csrf=; Max-Age=0; Path=/";
  await i18n.changeLanguage("en");
  localStorage.clear();
});
afterAll(() => server.close());
