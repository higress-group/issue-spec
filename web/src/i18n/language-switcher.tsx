import { Languages } from "lucide-react";
import { useTranslation } from "react-i18next";
import type { SupportedLanguage } from "./i18n";

export function LanguageSwitcher({ inverse = false }: { inverse?: boolean }) {
  const { i18n, t } = useTranslation();
  const language: SupportedLanguage = i18n.resolvedLanguage?.startsWith("zh") ? "zh-CN" : "en";
  return <label className={`language-switcher ${inverse ? "inverse" : ""}`.trim()}>
    <Languages size={16} aria-hidden="true" />
    <span className="sr-only">{t("language.label")}</span>
    <select aria-label={t("language.label")} value={language} onChange={(event) => void i18n.changeLanguage(event.target.value)}>
      <option value="en">{t("language.english")}</option>
      <option value="zh-CN">{t("language.chinese")}</option>
    </select>
  </label>;
}
