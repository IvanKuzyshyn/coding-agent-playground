"use client";

import { useRouter, useSearchParams } from "next/navigation";
import { useMemo, useOptimistic, useTransition } from "react";
import { SessionDrawer } from "@/components/session/session-drawer";
import { PAGE_SIZE, parseQuery, uiHref, type RunsQuery } from "@/lib/query";
import type { Session, SessionList } from "@/lib/types";
import { RunsTable } from "./runs-table";
import { Toolbar } from "./toolbar";

export function RunsView({
  list,
  openSession,
  openError,
}: {
  list: SessionList;
  openSession: Session | null;
  openError: string | null;
}) {
  const router = useRouter();
  const params = useSearchParams();
  const urlQuery = useMemo(() => parseQuery(params), [params]);
  const [pending, startTransition] = useTransition();
  // Controls and the drawer react immediately; the server catches up with the URL in the background.
  const [query, setQuery] = useOptimistic(urlQuery);
  const openId = query.session;

  const go = (next: RunsQuery, mode: "push" | "replace") =>
    startTransition(() => {
      setQuery(next);
      router[mode](uiHref(next), { scroll: false });
    });
  const update = (patch: Partial<RunsQuery>) => go({ ...query, page: 1, ...patch }, "replace");
  const open = (id: string) => go({ ...query, session: id }, "push");
  const close = () => go({ ...query, session: null }, "replace");
  const hrefFor = (id: string) => uiHref({ ...query, session: id });

  const drawerSession =
    (openSession?.id === openId ? openSession : null) ?? list.sessions.find((s) => s.id === openId) ?? null;
  const drawerError = openId && openId === urlQuery.session && !openSession ? openError : null;

  const first = list.total === 0 ? 0 : (urlQuery.page - 1) * PAGE_SIZE + 1;
  const last = Math.min(list.total, urlQuery.page * PAGE_SIZE);
  const filtered = query.q !== "" || query.status.length > 0 || query.harness.length > 0;

  return (
    <>
      <Toolbar query={query} update={update} pending={pending} />
      <RunsTable sessions={list.sessions} query={query} update={update} open={open} hrefFor={hrefFor} activeId={openId} />
      {list.sessions.length === 0 && (
        <p className="rounded-lg border border-line bg-panel px-4 py-10 text-center text-sm text-muted">
          {filtered ? "No runs match these filters." : "No runs yet. Sessions are created through the API."}
        </p>
      )}
      <nav aria-label="Pagination" className="flex items-center justify-between gap-3 text-sm text-muted">
        <span data-testid="range" className="tabular-nums">
          {first}–{last} of {list.total}
        </span>
        <div className="flex gap-2">
          <button
            type="button"
            disabled={query.page <= 1}
            onClick={() => go({ ...query, page: query.page - 1 }, "push")}
            className="rounded-md border border-line px-3 py-1 hover:bg-panel2 disabled:opacity-40"
          >
            Previous
          </button>
          <button
            type="button"
            disabled={last >= list.total}
            onClick={() => go({ ...query, page: query.page + 1 }, "push")}
            className="rounded-md border border-line px-3 py-1 hover:bg-panel2 disabled:opacity-40"
          >
            Next
          </button>
        </div>
      </nav>
      <SessionDrawer openId={openId} session={drawerSession} error={drawerError} onClose={close} />
    </>
  );
}
