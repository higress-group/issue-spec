import { useTranslation } from "react-i18next";

export const TOKEN_SCOPES = [
  "read:user",
  "read:org",
  "repo",
  "issues:read",
  "issues:write",
  "admin:org",
  "admin:repo",
  "evidence:write",
  "runner:delegate",
] as const;

export const RUNNER_TOKEN_SCOPES = ["read:user", "issues:read", "issues:write", "evidence:write"] as const;

export type TokenRepositoryOption = { id: string; label: string };

export function TokenScopeSelector({ value, onChange, error }: { value: string[]; onChange: (value: string[]) => void; error?: string }) {
  const { t } = useTranslation();
  const toggle = (scope: string, checked: boolean) => onChange(TOKEN_SCOPES.filter((candidate) => candidate === scope ? checked : value.includes(candidate)));
  return <fieldset className="token-authority-field" aria-invalid={Boolean(error)}>
    <legend>{t("tokenAuthority.scopes")}</legend>
    <div className="token-authority-toolbar"><small>{t("tokenAuthority.scopesHint")}</small><div className="button-row">
      <button className="button secondary small" type="button" onClick={() => onChange([...TOKEN_SCOPES])}>{t("tokenAuthority.selectAllScopes")}</button>
      <button className="button secondary small" type="button" onClick={() => onChange([])}>{t("tokenAuthority.clearScopes")}</button>
    </div></div>
    <div className="token-option-grid">{TOKEN_SCOPES.map((scope) => <label className="token-option" key={scope}>
      <input type="checkbox" checked={value.includes(scope)} onChange={(event) => toggle(scope, event.target.checked)} />
      <span className="mono">{scope}</span>
    </label>)}</div>
    {error ? <span className="field-error" role="alert">{error}</span> : null}
  </fieldset>;
}

export function RepositoryScopeSelector({ name, options, value, onChange, error }: {
  name: string;
  options: TokenRepositoryOption[];
  value: string[] | null;
  onChange: (value: string[] | null) => void;
  error?: string;
}) {
  const { t } = useTranslation();
  const restricted = value !== null;
  const toggle = (id: string, checked: boolean) => onChange(options.filter((option) => option.id === id ? checked : value?.includes(option.id)).map((option) => option.id));
  return <fieldset className="token-authority-field" aria-invalid={Boolean(error)}>
    <legend>{t("tokenAuthority.repositoryAccess")}</legend>
    <small>{t("tokenAuthority.repositoryHint")}</small>
    <div className="repository-mode-list">
      <label className="token-option repository-mode"><input type="radio" name={`${name}-mode`} checked={!restricted} onChange={() => onChange(null)} /><span>{t("tokenAuthority.allRepositories")}</span></label>
      <label className="token-option repository-mode"><input type="radio" name={`${name}-mode`} checked={restricted} disabled={options.length === 0} onChange={() => onChange(options.map((option) => option.id))} /><span>{t("tokenAuthority.selectedRepositories")}</span></label>
    </div>
    {restricted ? <div className="token-option-grid repository-option-grid">{options.map((option) => <label className="token-option" key={option.id}>
      <input type="checkbox" checked={value.includes(option.id)} onChange={(event) => toggle(option.id, event.target.checked)} />
      <span>{option.label}</span>
    </label>)}</div> : null}
    {error ? <span className="field-error" role="alert">{error}</span> : null}
  </fieldset>;
}
