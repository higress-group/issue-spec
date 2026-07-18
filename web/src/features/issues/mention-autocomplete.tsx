import { useEffect, useId, useRef, useState, type ChangeEvent, type KeyboardEvent } from "react";
import { Avatar } from "../../app/avatar";
import { issueApi, type MentionCandidate } from "./api";
import { useTranslation } from "react-i18next";

const debounceMilliseconds = 200;
const cacheMilliseconds = 30_000;
const maxCachedPrefixes = 20;

type ActiveMention = { start: number; end: number; query: string };

export function activeMentionAt(value: string, cursor: number): ActiveMention | null {
  if (cursor < 1 || cursor > value.length) return null;
  const before = value.slice(0, cursor);
  const start = before.lastIndexOf("@");
  if (start < 0) return null;
  const query = before.slice(start + 1);
  if (!query || Array.from(query).length > 64 || !/^[\p{L}\p{N}-]+$/u.test(query)) return null;
  if (start > 0 && /[\p{L}\p{N}_@/]/u.test(value[start - 1])) return null;
  return { start, end: cursor, query };
}

export function insertMention(value: string, mention: ActiveMention, login: string) {
  const inserted = `@${login} `;
  return { value: value.slice(0, mention.start) + inserted + value.slice(mention.end), cursor: mention.start + inserted.length };
}

export function MentionTextarea({ value, rows, label, placeholder, onChange }: {
  value: string;
  rows: number;
  label: string;
  placeholder: string;
  onChange: (value: string) => void;
}) {
  const { t } = useTranslation();
  const textarea = useRef<HTMLTextAreaElement>(null);
  const cache = useRef(new Map<string, { at: number; candidates: MentionCandidate[] }>());
  const [cursor, setCursor] = useState(value.length);
  const [candidates, setCandidates] = useState<MentionCandidate[]>([]);
  const [selected, setSelected] = useState(0);
  const [open, setOpen] = useState(false);
  const pendingCursor = useRef<number | null>(null);
  const listboxID = `${useId().replaceAll(":", "")}-mentions`;
  const active = activeMentionAt(value, cursor);
  const activeQuery = active?.query ?? "";
  const activeStart = active?.start ?? -1;
  const suggestionsOpen = active !== null && open;

  useEffect(() => {
    if (pendingCursor.current == null) return;
    const next = pendingCursor.current;
    pendingCursor.current = null;
    textarea.current?.focus();
    textarea.current?.setSelectionRange(next, next);
    setCursor(next);
  }, [value]);

  useEffect(() => {
    if (!activeQuery) return;
    const key = activeQuery.toLocaleLowerCase();
    const cached = cache.current.get(key);
    const controller = new AbortController();
    const timer = window.setTimeout(() => {
      if (cached && Date.now() - cached.at < cacheMilliseconds) {
        setCandidates(cached.candidates);
        setSelected(0);
        setOpen(cached.candidates.length > 0);
        return;
      }
      issueApi.mentionCandidates(activeQuery, controller.signal).then((result) => {
        if (controller.signal.aborted) return;
        cache.current.delete(key);
        cache.current.set(key, { at: Date.now(), candidates: result });
        while (cache.current.size > maxCachedPrefixes) cache.current.delete(cache.current.keys().next().value ?? "");
        setCandidates(result);
        setSelected(0);
        setOpen(result.length > 0);
      }).catch((error: unknown) => {
        if (!controller.signal.aborted && !(error instanceof DOMException && error.name === "AbortError")) {
          setCandidates([]);
          setOpen(false);
        }
      });
    }, cached ? 0 : debounceMilliseconds);
    return () => {
      window.clearTimeout(timer);
      controller.abort();
    };
  }, [activeQuery, activeStart]);

  const choose = (candidate: MentionCandidate) => {
    const current = activeMentionAt(value, cursor);
    if (!current) return;
    const result = insertMention(value, current, candidate.login);
    pendingCursor.current = result.cursor;
    onChange(result.value);
    setCandidates([]);
    setOpen(false);
  };
  const keyDown = (event: KeyboardEvent<HTMLTextAreaElement>) => {
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
  const changed = (event: ChangeEvent<HTMLTextAreaElement>) => {
    onChange(event.target.value);
    setCursor(event.target.selectionStart ?? event.target.value.length);
  };
  const selectionChanged = () => setCursor(textarea.current?.selectionStart ?? value.length);

  return <div className="mention-editor">
    <textarea
      ref={textarea}
      aria-label={label}
      aria-autocomplete="list"
      aria-controls={suggestionsOpen ? listboxID : undefined}
      aria-activedescendant={suggestionsOpen ? `${listboxID}-${selected}` : undefined}
      value={value}
      rows={rows}
      placeholder={placeholder}
      onChange={changed}
      onClick={selectionChanged}
      onKeyUp={selectionChanged}
      onKeyDown={keyDown}
      onBlur={() => window.setTimeout(() => setOpen(false), 0)}
    />
    {suggestionsOpen ? <ul className="mention-suggestions" id={listboxID} role="listbox" aria-label={t("mentions.suggestions", { defaultValue: "Mention suggestions" })}>
      {candidates.map((candidate, index) => <li
        id={`${listboxID}-${index}`}
        key={candidate.login}
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
