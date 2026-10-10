"use client";

import Link from "next/link";
import type { MouseEvent, ReactNode } from "react";
import { Cost } from "@/components/cost";
import { HarnessIcon } from "@/components/harness-icon";
import { LiveDuration, LocalTime } from "@/components/live";
import { RelTime } from "@/components/rel-time";
import { StatusDot } from "@/components/status-dot";
import { shortId } from "@/lib/format";
import { DEFAULT_ORDER, type RunsQuery, type SortKey } from "@/lib/query";
import type { Session } from "@/lib/types";

interface Props {
  sessions: Session[];
  query: RunsQuery;
  update: (patch: Partial<RunsQuery>) => void;
  open: (id: string) => void;
  hrefFor: (id: string) => string;
  activeId: string | null;
}

export function RunsTable({ sessions, query, update, open, hrefFor, activeId }: Props) {
  if (sessions.length === 0) return null;

  // Plain clicks open the drawer in place; modified clicks keep the link's new-tab behaviour.
  const onLink = (e: MouseEvent, id: string) => {
    e.stopPropagation();
    if (e.metaKey || e.ctrlKey || e.shiftKey || e.altKey || e.button !== 0) return;
    e.preventDefault();
    open(id);
  };

  const sortHeader = (key: SortKey, label: string, className = "") => {
    const active = query.sort === key;
    return (
      <th
        scope="col"
        aria-sort={active ? (query.order === "asc" ? "ascending" : "descending") : "none"}
        className={`px-3 py-2 font-medium ${className}`}
      >
        <button
          type="button"
          onClick={() => update({ sort: key, order: active ? (query.order === "asc" ? "desc" : "asc") : DEFAULT_ORDER[key] })}
          className={`inline-flex items-center gap-1 tracking-widest uppercase hover:text-fg ${active ? "text-fg" : ""}`}
        >
          {label}
          <span aria-hidden className={active ? "text-accent" : "opacity-40"}>
            {active && query.order === "asc" ? "↑" : "↓"}
          </span>
        </button>
      </th>
    );
  };

  return (
    <>
      <div className="hidden overflow-hidden rounded-lg border border-line md:block">
        <table className="w-full border-collapse text-sm">
          <thead className="bg-panel text-left text-[11px] text-muted">
            <tr className="border-b border-line">
              {sortHeader("created_at", "When", "w-36")}
              {sortHeader("status", "Status", "w-32")}
              {sortHeader("name", "Run")}
              <th scope="col" className="w-32 px-3 py-2 font-medium tracking-widest uppercase">
                Owner
              </th>
              <th scope="col" className="w-20 px-3 py-2 font-medium tracking-widest uppercase">
                Harness
              </th>
              {sortHeader("duration", "Duration", "w-28")}
              {sortHeader("cost", "Cost", "w-28")}
            </tr>
          </thead>
          <tbody>
            {sessions.map((s) => (
              <tr
                key={s.id}
                data-session-row={s.id}
                onClick={() => open(s.id)}
                className={`cursor-pointer border-b border-line/70 last:border-0 hover:bg-panel2/70 ${
                  activeId === s.id ? "bg-panel2" : ""
                }`}
              >
                <td className="px-3 py-2.5 align-middle">
                  <LocalTime iso={s.created_at} className="block whitespace-nowrap text-fg" />
                  <span className="text-xs text-muted">
                    <RelTime iso={s.created_at} />
                  </span>
                </td>
                <td className="px-3 py-2.5 align-middle">
                  <StatusDot status={s.display_status} />
                </td>
                <td className="max-w-0 px-3 py-2.5 align-middle">
                  <RunName s={s} href={hrefFor(s.id)} onClick={(e) => onLink(e, s.id)} />
                </td>
                <td className="max-w-0 truncate px-3 py-2.5 align-middle font-mono text-xs text-muted" title={s.owner}>
                  {s.owner}
                </td>
                <td className="px-3 py-2.5 align-middle">
                  <HarnessIcon harness={s.harness} />
                </td>
                <td className="px-3 py-2.5 align-middle text-fg">
                  <LiveDuration session={s} />
                </td>
                <td className="px-3 py-2.5 align-middle">
                  <Cost session={s} />
                </td>
              </tr>
            ))}
          </tbody>
        </table>
      </div>

      <ul className="flex flex-col gap-2 md:hidden">
        {sessions.map((s) => (
          <li
            key={s.id}
            data-session-row={s.id}
            onClick={() => open(s.id)}
            className={`flex cursor-pointer flex-col gap-2 rounded-lg border border-line p-3 ${activeId === s.id ? "bg-panel2" : "bg-panel"}`}
          >
            <div className="flex items-start justify-between gap-3">
              <div className="min-w-0 flex-1">
                <RunName s={s} href={hrefFor(s.id)} onClick={(e) => onLink(e, s.id)} />
              </div>
              <HarnessIcon harness={s.harness} size={24} />
            </div>
            <div className="flex flex-wrap items-center gap-x-4 gap-y-1 text-xs">
              <StatusDot status={s.display_status} />
              <Meta>
                <LocalTime iso={s.created_at} /> · <RelTime iso={s.created_at} />
              </Meta>
              <Meta>
                <LiveDuration session={s} />
              </Meta>
              <Meta>{s.owner}</Meta>
            </div>
            <Cost session={s} />
          </li>
        ))}
      </ul>
    </>
  );
}

function RunName({ s, href, onClick }: { s: Session; href: string; onClick: (e: MouseEvent) => void }) {
  return (
    <div className="min-w-0">
      <Link href={href} scroll={false} onClick={onClick} className="block truncate font-medium text-fg hover:underline" title={s.name}>
        {s.name}
      </Link>
      <div className="truncate font-mono text-xs text-muted">
        {shortId(s.id)}
        {s.repo && <> · {s.repo}</>}
      </div>
      {s.error && (
        <div className="truncate text-xs text-red-300/90" title={s.error}>
          {s.error}
        </div>
      )}
    </div>
  );
}

function Meta({ children }: { children: ReactNode }) {
  return <span className="text-muted">{children}</span>;
}
