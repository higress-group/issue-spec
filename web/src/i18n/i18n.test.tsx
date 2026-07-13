import { render, screen } from "@testing-library/react";
import userEvent from "@testing-library/user-event";
import { describe, expect, it } from "vitest";
import i18n, { languageStorageKey, resolveLanguage } from "./i18n";
import { LanguageSwitcher } from "./language-switcher";

describe("language configuration", () => {
  it("prefers an explicit stored language and otherwise recognizes Chinese browser locales", () => {
    expect(resolveLanguage("en", ["zh-CN"])).toBe("en");
    expect(resolveLanguage("zh-TW", ["en-US"])).toBe("zh-CN");
    expect(resolveLanguage(null, ["zh-Hans-CN", "en-US"])).toBe("zh-CN");
    expect(resolveLanguage(null, ["en-US"])).toBe("en");
  });

  it("switches to Chinese, persists the choice, and synchronizes the document language", async () => {
    render(<LanguageSwitcher />);
    await userEvent.setup().selectOptions(screen.getByRole("combobox", { name: "Language" }), "zh-CN");
    expect(i18n.resolvedLanguage).toBe("zh-CN");
    expect(localStorage.getItem(languageStorageKey)).toBe("zh-CN");
    expect(document.documentElement.lang).toBe("zh-CN");
    expect(screen.getByRole("combobox", { name: "语言" })).toHaveValue("zh-CN");
  });
});
