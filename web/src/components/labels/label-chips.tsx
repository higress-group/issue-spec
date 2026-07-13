import type { Label } from "../../features/issues/types";
import { useTranslation } from "react-i18next";
import "./labels.css";

export function LabelChips({ labels }: { labels: Label[] }) {
  const { t } = useTranslation();
  if (!labels.length) return null;
  return <span className="label-chips" aria-label={t("issues.labels.aria")}>{labels.map((label) => <span className="label-chip" key={label.id} title={label.description || label.name}><span aria-hidden="true" style={{ backgroundColor: `#${label.color}` }} />{label.name}</span>)}</span>;
}
export function LabelSelector({ labels, selected, disabled, onChange }: { labels: Label[]; selected: string[]; disabled?: boolean; onChange: (names: string[]) => void }) {
  const { t } = useTranslation();
  if (!labels.length) return <p className="field-note">{t("issues.labels.empty")}</p>;
  return <fieldset className="label-selector" disabled={disabled}><legend>{t("issues.labels.aria")}</legend>{labels.map((label) => {
    const checked = selected.includes(label.name);
    return <label key={label.id}><input type="checkbox" checked={checked} onChange={() => onChange(checked ? selected.filter((name) => name !== label.name) : [...selected, label.name])} /><span aria-hidden="true" style={{ backgroundColor: `#${label.color}` }} /><span>{label.name}</span></label>;
  })}</fieldset>;
}
