"use client";

import { useEffect, useState, useSyncExternalStore } from "react";
import { absTime, formatDuration } from "@/lib/format";
import type { Session } from "@/lib/types";

const noopSubscribe = () => () => {};

// False during SSR and hydration, true afterwards: lets time-zone dependent text re-render on the
// client instead of keeping the server's (UTC) rendering.
function useHydrated(): boolean {
  return useSyncExternalStore(
    noopSubscribe,
    () => true,
    () => false,
  );
}

// Current time, updated every `ms`; null until the first tick so server and client agree.
function useNow(ms: number, enabled = true): number | null {
  const [now, setNow] = useState<number | null>(null);
  useEffect(() => {
    if (!enabled) return;
    const t = setInterval(() => setNow(Date.now()), ms);
    return () => clearInterval(t);
  }, [ms, enabled]);
  return enabled ? now : null;
}

export function LocalTime({ iso, className }: { iso: string; className?: string }) {
  const hydrated = useHydrated();
  return (
    <time dateTime={iso} title={iso} className={className}>
      {hydrated ? absTime(iso) : iso.slice(0, 16).replace("T", " ")}
    </time>
  );
}

const ENDED: Session["display_status"][] = ["stopped", "failed"];

// Ticks every second while the session is not stopped; the API value is authoritative otherwise.
export function LiveDuration({ session }: { session: Pick<Session, "duration_seconds" | "display_status" | "created_at"> }) {
  const live = !ENDED.includes(session.display_status);
  const now = useNow(1000, live);
  const seconds =
    live && now !== null
      ? Math.max(session.duration_seconds, (now - new Date(session.created_at).getTime()) / 1000)
      : session.duration_seconds;
  return <span className="tabular-nums">{formatDuration(seconds)}</span>;
}
