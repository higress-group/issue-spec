import type { Page } from "@playwright/test";

declare const process: { env: Record<string, string | undefined> };

export const documentationLanguage = process.env.ISSUE_SPEC_E2E_LANGUAGE === "zh-CN" ? "zh-CN" : "en";
export const isChineseDocumentation = documentationLanguage === "zh-CN";

export function documentationText(english: string, chinese: string) {
  return isChineseDocumentation ? chinese : english;
}

export function documentationSnapshot(name: string) {
  return `${name}${isChineseDocumentation ? ".zh-CN" : ""}.png`;
}

export async function installDocumentationLanguage(page: Page) {
  await page.addInitScript((language) => {
    window.localStorage.setItem("issue-spec.language", language);
  }, documentationLanguage);
}
