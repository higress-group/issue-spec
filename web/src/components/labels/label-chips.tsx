import type { Label } from "../../features/issues/types";
import "./labels.css";

export function LabelChips({ labels }: { labels: Label[] }) {
  if (!labels.length) return null;
  return <span className="label-chips" aria-label="Labels">{labels.map((label) => <span className="label-chip" key={label.id} title={label.description || label.name}><span aria-hidden="true" style={{ backgroundColor: `#${label.color}` }} />{label.name}</span>)}</span>;
}
export function LabelSelector({ labels, selected, disabled, onChange }: { labels: Label[]; selected: string[]; disabled?: boolean; onChange: (names: string[]) => void }) {
  if (!labels.length) return <p className="field-note">This repository has no labels yet.</p>;
  return <fieldset className="label-selector" disabled={disabled}><legend>Labels</legend>{labels.map((label) => {
    const checked = selected.includes(label.name);
    return <label key={label.id}><input type="checkbox" checked={checked} onChange={() => onChange(checked ? selected.filter((name) => name !== label.name) : [...selected, label.name])} /><span aria-hidden="true" style={{ backgroundColor: `#${label.color}` }} /><span>{label.name}</span></label>;
  })}</fieldset>;
}
