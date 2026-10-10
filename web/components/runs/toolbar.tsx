"use client";

import { useEffect, useId, useRef, useState } from "react";
import { HARNESS_LABELS } from "@/components/harness-icon";
import { StatusDot } from "@/components/status-dot";
import { DEFAULT_ORDER, SORTS, SORT_LABELS, type RunsQuery, type SortKey } from "@/lib/query";
import { DISPLAY_STATUSES, HARNESSES } from "@/lib/types";

const SEARCH_DEBOUNCE_MS = 300;

export function Toolbar({
  query,
  update,
  pending,
}: {
  query: RunsQuery;
  update: (patch: Partial<RunsQuery>) => void;
  pending: boolean;
}) {
  return (
    <div className="flex flex-wrap items-center gap-2">
      <SearchBox value={query.q} onSearch={(q) => update({ q })} />
      <MultiSelect
        label="Status"
        options={DISPLAY_STATUSES}
        selected={query.status}
        render={(v) => <StatusDot status={v} />}
        onChange={(status) => update({ status })}
      />
      <MultiSelect
        label="Harness"
        options={HARNESSES}
        selected={query.harness}
        render={(v) => HARNESS_LABELS[v]}
        onChange={(harness) => update({ harness })}
      />
      <div className="ml-auto flex items-center gap-2">
        <span aria-live="polite" className="w-4 text-muted">
          {pending && <span className="block h-3 w-3 animate-spin rounded-full border border-muted border-t-transparent" aria-label="Loading" />}
        </span>
        <label className="flex h-9 items-center gap-2 rounded-md border border-line bg-panel px-2.5 text-sm">
          <span className="text-muted">Sort</span>
          <select
            aria-label="Sort by"
            value={query.sort}
            onChange={(e) => {
              const sort = e.target.value as SortKey;
              update({ sort, order: DEFAULT_ORDER[sort] });
            }}
            className="bg-transparent text-fg outline-none"
          >
            {SORTS.map((s) => (
              <option key={s} value={s} className="bg-panel">
                {SORT_LABELS[s]}
              </option>
            ))}
          </select>
        </label>
        <button
          type="button"
          onClick={() => update({ order: query.order === "asc" ? "desc" : "asc" })}
          aria-label={query.order === "asc" ? "Ascending, switch to descending" : "Descending, switch to ascending"}
          title={query.order === "asc" ? "Ascending" : "Descending"}
          className="flex h-9 items-center gap-1.5 rounded-md border border-line bg-panel px-2.5 text-sm text-fg hover:bg-panel2"
        >
          <svg viewBox="0 0 16 16" width="14" height="14" fill="none" stroke="currentColor" strokeWidth="1.4" strokeLinecap="round" aria-hidden>
            {query.order === "asc" ? <path d="M4 12V4M1.5 6.5 4 4l2.5 2.5M9 5h5M9 8h4M9 11h3" /> : <path d="M4 4v8M1.5 9.5 4 12l2.5-2.5M9 5h3M9 8h4M9 11h5" />}
          </svg>
          {query.order === "asc" ? "Asc" : "Desc"}
        </button>
      </div>
    </div>
  );
}

function SearchBox({ value, onSearch }: { value: string; onSearch: (q: string) => void }) {
  const [text, setText] = useState(value);
  const [synced, setSynced] = useState(value);
  const [sent, setSent] = useState(value);
  // Follow URL changes made elsewhere (back button, shared link) without clobbering typing that
  // continued while our own search was loading.
  if (value !== synced) {
    setSynced(value);
    if (value !== sent) setText(value);
  }
  const onSearchRef = useRef(onSearch);
  useEffect(() => {
    onSearchRef.current = onSearch;
  });
  useEffect(() => {
    const q = text.trim();
    if (q === value) return;
    const t = setTimeout(() => {
      setSent(q);
      onSearchRef.current(q);
    }, SEARCH_DEBOUNCE_MS);
    return () => clearTimeout(t);
  }, [text, value]);
  return (
    <label className="flex h-9 min-w-0 flex-1 basis-56 items-center gap-2 rounded-md border border-line bg-panel px-2.5 focus-within:border-accent sm:max-w-md">
      <svg viewBox="0 0 16 16" width="14" height="14" fill="none" stroke="currentColor" strokeWidth="1.4" className="shrink-0 text-muted" aria-hidden>
        <circle cx="7" cy="7" r="4.5" />
        <path d="m10.5 10.5 3.5 3.5" strokeLinecap="round" />
      </svg>
      <input
        type="search"
        aria-label="Search runs by name"
        placeholder="Search runs by name…"
        value={text}
        onChange={(e) => setText(e.target.value)}
        className="min-w-0 flex-1 bg-transparent text-sm text-fg outline-none placeholder:text-muted"
      />
    </label>
  );
}

function MultiSelect<T extends string>({
  label,
  options,
  selected,
  render,
  onChange,
}: {
  label: string;
  options: readonly T[];
  selected: T[];
  render: (v: T) => React.ReactNode;
  onChange: (v: T[]) => void;
}) {
  const [open, setOpen] = useState(false);
  const ref = useRef<HTMLDivElement>(null);
  const id = useId();
  useEffect(() => {
    if (!open) return;
    const onDown = (e: MouseEvent) => {
      if (!ref.current?.contains(e.target as Node)) setOpen(false);
    };
    const onKey = (e: KeyboardEvent) => {
      if (e.key === "Escape") setOpen(false);
    };
    document.addEventListener("mousedown", onDown);
    document.addEventListener("keydown", onKey);
    return () => {
      document.removeEventListener("mousedown", onDown);
      document.removeEventListener("keydown", onKey);
    };
  }, [open]);
  const toggle = (v: T) =>
    onChange(options.filter((o) => (o === v ? !selected.includes(o) : selected.includes(o))));
  return (
    <div ref={ref} className="relative">
      <button
        type="button"
        aria-expanded={open}
        aria-controls={id}
        onClick={() => setOpen((o) => !o)}
        className={`flex h-9 items-center gap-1.5 rounded-md border px-2.5 text-sm hover:bg-panel2 ${
          selected.length ? "border-accent/60 bg-accent/10 text-fg" : "border-line bg-panel text-fg"
        }`}
      >
        {label}
        {selected.length > 0 && <span className="rounded bg-accent/20 px-1 font-mono text-xs text-accent">{selected.length}</span>}
        <svg viewBox="0 0 16 16" width="12" height="12" fill="none" stroke="currentColor" strokeWidth="1.5" className="text-muted" aria-hidden>
          <path d="m4 6 4 4 4-4" strokeLinecap="round" strokeLinejoin="round" />
        </svg>
      </button>
      {open && (
        <div
          id={id}
          role="group"
          aria-label={`${label} filter`}
          className="absolute left-0 z-30 mt-1 min-w-48 rounded-md border border-line bg-panel p-1 shadow-xl shadow-black/40"
        >
          {options.map((o) => (
            <label key={o} className="flex cursor-pointer items-center gap-2 rounded px-2 py-1.5 text-sm hover:bg-panel2">
              <input type="checkbox" checked={selected.includes(o)} onChange={() => toggle(o)} className="accent-sky-500" value={o} />
              {render(o)}
            </label>
          ))}
          {selected.length > 0 && (
            <button
              type="button"
              onClick={() => onChange([])}
              className="mt-1 w-full rounded border-t border-line px-2 py-1.5 text-left text-xs text-muted hover:text-fg"
            >
              Clear
            </button>
          )}
        </div>
      )}
    </div>
  );
}
