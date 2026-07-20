import { useEffect, useId, useRef, useState, type ChangeEvent, type KeyboardEvent } from "react";
import { useTranslation } from "react-i18next";
import { Avatar } from "./avatar";
import { api } from "../lib/api/resources";
import type { UserCandidate } from "../lib/api/types";

const debounceMilliseconds = 200;
const maximumResults = 10;

export function UserAutocomplete({ orgId, purpose, label, value, onChange, onSelect, disabled = false }: {
  orgId: string;
  purpose: "membership";
  label: string;
  value: string;
  onChange: (value: string) => void;
  onSelect: (candidate: UserCandidate) => void;
  disabled?: boolean;
}) {
  const { t } = useTranslation();
  const input = useRef<HTMLInputElement>(null);
  const skipSearch = useRef(false);
  const [candidates, setCandidates] = useState<UserCandidate[]>([]);
  const [selected, setSelected] = useState(0);
  const [open, setOpen] = useState(false);
  const listboxID = `${useId().replaceAll(":", "")}-user-suggestions`;
  const query = value.trim();
  const suggestionsOpen = query !== "" && open;

  useEffect(() => {
    if (skipSearch.current) {
      skipSearch.current = false;
      return;
    }
    if (!query) return;
    const controller = new AbortController();
    const timer = window.setTimeout(() => {
      api.userCandidates(orgId, purpose, query, "prefix", controller.signal, maximumResults).then((result) => {
        if (controller.signal.aborted) return;
        setCandidates(result.users);
        setSelected(0);
        setOpen(result.users.length > 0);
      }).catch((error: unknown) => {
        if (controller.signal.aborted || (error instanceof DOMException && error.name === "AbortError")) return;
        setCandidates([]);
        setOpen(false);
      });
    }, debounceMilliseconds);
    return () => {
      window.clearTimeout(timer);
      controller.abort();
    };
  }, [orgId, purpose, query]);

  const choose = (candidate: UserCandidate) => {
    skipSearch.current = true;
    onChange(candidate.login);
    onSelect(candidate);
    setCandidates([]);
    setOpen(false);
    input.current?.focus();
  };
  const keyDown = (event: KeyboardEvent<HTMLInputElement>) => {
    if (!suggestionsOpen || candidates.length === 0) return;
    if (event.key === "ArrowDown" || event.key === "ArrowUp") {
      event.preventDefault();
      const direction = event.key === "ArrowDown" ? 1 : -1;
      setSelected((current) => (current + direction + candidates.length) % candidates.length);
    } else if (event.key === "Enter" || event.key === "Tab") {
      event.preventDefault();
      choose(candidates[selected]);
    } else if (event.key === "Escape") {
      event.preventDefault();
      setOpen(false);
    }
  };
  const changed = (event: ChangeEvent<HTMLInputElement>) => {
    onChange(event.target.value);
    setOpen(false);
  };

  return <div className="user-autocomplete">
    <input
      ref={input}
      className="input"
      aria-label={label}
      aria-autocomplete="list"
      aria-controls={suggestionsOpen ? listboxID : undefined}
      aria-activedescendant={suggestionsOpen ? `${listboxID}-${selected}` : undefined}
      value={value}
      disabled={disabled}
      onChange={changed}
      onKeyDown={keyDown}
      onBlur={() => window.setTimeout(() => setOpen(false), 0)}
    />
    {suggestionsOpen ? <ul className="user-candidate-suggestions" id={listboxID} role="listbox" aria-label={t("members.suggestions")}>
      {candidates.map((candidate, index) => <li
        id={`${listboxID}-${index}`}
        key={candidate.id}
        role="option"
        aria-selected={selected === index}
        onMouseDown={(event) => { event.preventDefault(); choose(candidate); }}
      >
        <Avatar login={candidate.login} displayName={candidate.display_name} src={candidate.avatar_url} size={32} />
        <span><strong>{candidate.display_name}</strong><small>@{candidate.login}</small></span>
      </li>)}
    </ul> : null}
  </div>;
}
