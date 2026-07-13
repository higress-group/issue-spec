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

  it("uses product-specific Chinese terminology across administration and repository surfaces", async () => {
    await i18n.changeLanguage("zh-CN");
    expect(i18n.t("navigation.repositories")).toBe("仓库");
    expect(i18n.t("navigation.administration")).toBe("管理后台");
    expect(i18n.t("members.title")).toBe("组织成员");
    expect(i18n.t("collaborators.title")).toBe("仓库协作者");
    expect(i18n.t("managedTokens.title")).toBe("托管访问令牌");
    expect(i18n.t("integrations.sourceTitle")).toBe("源仓库连接");
    expect(i18n.t("integrations.webhooksTitle")).toBe("Webhook 投递管理");
    expect(i18n.t("common.contribution.members")).toBe("仅组织成员");
    expect(i18n.t("common.invited")).toBe("待接受");
    expect(i18n.t("common.suspended")).toBe("已暂停");
    expect(i18n.t("integrations.activeRoutes", { count: 2 })).toBe("2 条启用中的路由");
    expect(i18n.t("issues.workspace.eyebrow")).toBe("议题工作台");
    expect(i18n.t("issues.workspace.title")).toBe("切问而近思。");
    expect(i18n.t("issues.list.newIssue")).toBe("新建议题");
    expect(i18n.t("issues.detail.continueConversation")).toBe("继续讨论");
    expect(i18n.t("changes.workspace.eyebrow")).toBe("变更工作台");
    expect(i18n.t("changes.workspace.title")).toBe("穷则变，变则通，通则久。");
    expect(i18n.t("changes.board.currentProjection")).toBe("当前汇总");
    expect(i18n.t("changes.detail.artifactChain")).toBe("产物链");
    expect(i18n.t("changes.detail.diagnostics")).toBe("结构诊断");
    expect(i18n.t("changes.lifecycle.blocked")).toBe("受阻");
    await i18n.changeLanguage("en");
    expect(i18n.t("integrations.activeRoutes", { count: 1 })).toBe("1 active route");
    expect(i18n.t("integrations.activeRoutes", { count: 2 })).toBe("2 active routes");
  });
});
