import i18n from "i18next";
import { initReactI18next } from "react-i18next";
import { resources } from "./resources";

export const supportedLanguages = ["en", "zh-CN"] as const;
export type SupportedLanguage = (typeof supportedLanguages)[number];
export const languageStorageKey = "issue-spec.language";

export function resolveLanguage(stored?: string | null, browserLanguages: readonly string[] = []): SupportedLanguage {
  if (stored) return stored.toLowerCase().startsWith("zh") ? "zh-CN" : "en";
  return browserLanguages.some((value) => value.toLowerCase().startsWith("zh")) ? "zh-CN" : "en";
}

const initialLanguage = resolveLanguage(
  typeof window === "undefined" ? null : window.localStorage.getItem(languageStorageKey),
  typeof navigator === "undefined" ? [] : navigator.languages,
);

void i18n.use(initReactI18next).init({
  resources,
  lng: initialLanguage,
  fallbackLng: "en",
  supportedLngs: supportedLanguages,
  interpolation: { escapeValue: false },
});

function syncDocumentLanguage(language: string) {
  if (typeof document !== "undefined") document.documentElement.lang = resolveLanguage(language);
}

syncDocumentLanguage(initialLanguage);
i18n.on("languageChanged", (language) => {
  const resolved = resolveLanguage(language);
  if (typeof window !== "undefined") window.localStorage.setItem(languageStorageKey, resolved);
  syncDocumentLanguage(resolved);
});

export default i18n;
