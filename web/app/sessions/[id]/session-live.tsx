"use client";

import { useRouter } from "next/navigation";
import { useActionState, useEffect, useMemo, useRef, useState } from "react";
import { deleteSession, followUp, stopTurn, type ActionState } from "@/app/actions";
import { StatusBadge } from "@/components/status-badge";
import { formatTokens } from "@/lib/format";
import { TERMINAL_SESSION, type EgressPayload, type Session, type SessionEvent, type Turn } from "@/lib/types";
import { EventItem } from "./event-item";

const KINDS = ["lifecycle", "harness", "stderr", "egress", "system", "message"];
type Conn = "connecting" | "live" | "reconnecting" | "ended";

function useEventStream(sessionId: string, initiallyStopped: boolean) {
  const router = useRouter();
  const [events, setEvents] = useState<SessionEvent[]>([]);
  const [conn, setConn] = useState<Conn>("connecting");
  const stoppedRef = useRef(initiallyStopped);

  useEffect(() => {
    let es: EventSource | null = null;
    let lastId = 0;
    let retry: ReturnType<typeof setTimeout> | undefined;
    let refresh: ReturnType<typeof setTimeout> | undefined;
    const seen = new Set<number>();

    const scheduleRefresh = () => {
      clearTimeout(refresh);
      refresh = setTimeout(() => router.refresh(), 300);
    };

    const onEvent = (m: MessageEvent<string>) => {
      let ev: SessionEvent;
      try {
        ev = JSON.parse(m.data) as SessionEvent;
      } catch {
        return;
      }
      if (seen.has(ev.id)) return;
      seen.add(ev.id);
      lastId = Math.max(lastId, ev.id);
      setEvents((prev) => [...prev, ev]);
      if (ev.kind === "lifecycle") {
        if (ev.payload.status === "stopped") stoppedRef.current = true;
        scheduleRefresh();
      }
    };

    const connect = () => {
      // Native EventSource resends Last-Event-ID on its own retries; `after` covers manual reconnects.
      es = new EventSource(`/api/sessions/${sessionId}/events?after=${lastId}`);
      es.onopen = () => setConn("live");
      for (const k of KINDS) es.addEventListener(k, onEvent as EventListener);
      es.onerror = () => {
        // The API ends the stream once a stopped session is flushed; don't reconnect forever.
        if (stoppedRef.current) {
          es?.close();
          setConn("ended");
          return;
        }
        if (es?.readyState === EventSource.CLOSED) {
          setConn("reconnecting");
          retry = setTimeout(connect, 3000);
        } else {
          setConn("reconnecting");
        }
      };
    };
    connect();

    return () => {
      es?.close();
      clearTimeout(retry);
      clearTimeout(refresh);
    };
  }, [sessionId, router]);

  return { events, conn };
}

export function SessionLive({ session }: { session: Session }) {
  const { events, conn } = useEventStream(session.id, session.status === "stopped");
  const turns = useMemo(() => groupTurns(session.turns ?? [], events), [session.turns, events]);
  const sessionEvents = events.filter((e) => !e.turn_seq);
  const denied = events.filter((e) => e.kind === "egress" && e.payload.decision === "denied");
  const allowedCount = events.filter((e) => e.kind === "egress" && e.payload.decision === "allowed").length;
  const completedItems = useMemo(() => {
    const s = new Set<string>();
    for (const e of events) {
      const ev = (e.payload.event ?? {}) as { type?: string; item?: { id?: string } };
      if (e.kind === "harness" && ev.type === "item.completed" && ev.item?.id) s.add(ev.item.id);
    }
    return s;
  }, [events]);

  useFollowTail(events.length);

  const running = session.status === "running";
  const closed = TERMINAL_SESSION.includes(session.status);

  return (
    <div className="grid grid-cols-1 gap-3 lg:grid-cols-[1fr_320px]">
      <div className="order-2 flex min-w-0 flex-col gap-3 lg:order-1">
        <div className="flex items-center justify-between text-xs text-muted">
          <span className="font-mono">
            {events.length} events · stream <ConnDot conn={conn} />
          </span>
        </div>

        {sessionEvents.length > 0 && (
          <details className="rounded border border-line bg-panel">
            <summary className="px-3 py-1.5 font-mono text-xs text-muted">
              session events ({sessionEvents.length})
            </summary>
            <div className="border-t border-line px-2 py-1">
              {sessionEvents.map((ev) => (
                <EventItem key={ev.id} ev={ev} completedItems={completedItems} />
              ))}
            </div>
          </details>
        )}

        {turns.map(({ seq, turn, events: tEvents }) => (
          <section key={seq} className="rounded border border-line bg-panel">
            <div className="flex flex-wrap items-center gap-2 border-b border-line px-3 py-1.5">
              <span className="font-mono text-xs text-fg">turn {seq}</span>
              {turn && <StatusBadge status={turn.status} />}
              {turn?.usage && (turn.usage.input_tokens > 0 || turn.usage.output_tokens > 0) && (
                <span className="font-mono text-[11px] text-muted">
                  in {formatTokens(turn.usage.input_tokens)} · cached {formatTokens(turn.usage.cached_input_tokens)} · out{" "}
                  {formatTokens(turn.usage.output_tokens)} · {turn.usage.agent_steps} steps
                </span>
              )}
            </div>
            {turn && (
              <p className="border-b border-line bg-panel2 px-3 py-2 text-sm whitespace-pre-wrap break-words">
                <span className="mr-2 font-mono text-xs text-accent">prompt</span>
                {turn.prompt}
              </p>
            )}
            <div className="px-2 py-1">
              {tEvents.length === 0 ? (
                <p className="px-1 py-1 font-mono text-xs text-muted">no events yet</p>
              ) : (
                tEvents.map((ev) => <EventItem key={ev.id} ev={ev} completedItems={completedItems} />)
              )}
            </div>
            {turn?.error && (
              <p className="border-t border-line px-3 py-1.5 font-mono text-xs text-red-300">{turn.error}</p>
            )}
          </section>
        ))}

        <FollowUp sessionId={session.id} disabled={closed} running={running || session.current_turn !== undefined} />
      </div>

      <aside className="order-1 flex flex-col gap-3 lg:order-2">
        <Controls sessionId={session.id} running={running} closed={closed} />
        <EgressPanel denied={denied} allowedCount={allowedCount} />
      </aside>
    </div>
  );
}

function groupTurns(turns: Turn[], events: SessionEvent[]) {
  const map = new Map<number, { seq: number; turn?: Turn; events: SessionEvent[] }>();
  for (const t of turns) map.set(t.seq, { seq: t.seq, turn: t, events: [] });
  for (const e of events) {
    if (!e.turn_seq) continue;
    let g = map.get(e.turn_seq);
    if (!g) map.set(e.turn_seq, (g = { seq: e.turn_seq, events: [] }));
    g.events.push(e);
  }
  return [...map.values()].sort((a, b) => a.seq - b.seq);
}

// Keep the newest events in view while the engineer is already at the bottom of the page.
function useFollowTail(count: number) {
  const atBottom = useRef(true);
  useEffect(() => {
    const onScroll = () => {
      atBottom.current = window.innerHeight + window.scrollY >= document.body.scrollHeight - 120;
    };
    window.addEventListener("scroll", onScroll, { passive: true });
    return () => window.removeEventListener("scroll", onScroll);
  }, []);
  useEffect(() => {
    if (count > 0 && atBottom.current) window.scrollTo({ top: document.body.scrollHeight });
  }, [count]);
}

function ConnDot({ conn }: { conn: Conn }) {
  const color = { connecting: "text-amber-300", live: "text-emerald-300", reconnecting: "text-amber-300", ended: "text-muted" }[conn];
  return <span className={color}>● {conn}</span>;
}

function Msg({ state }: { state: ActionState }) {
  if (state.error) return <p className="text-xs text-red-400">{state.error}</p>;
  if (state.ok) return <p className="text-xs text-muted">{state.ok}</p>;
  return null;
}

function Controls({ sessionId, running, closed }: { sessionId: string; running: boolean; closed: boolean }) {
  const [stopState, stopAction, stopping] = useActionState(stopTurn, {});
  const [delState, delAction, deleting] = useActionState(deleteSession, {});
  return (
    <div className="flex flex-col gap-2 rounded border border-line bg-panel p-3">
      <div className="flex flex-wrap gap-2">
        {running && (
          <form action={stopAction}>
            <input type="hidden" name="id" value={sessionId} />
            <button
              disabled={stopping}
              className="rounded border border-orange-500/60 bg-orange-500/10 px-3 py-1.5 text-sm text-orange-200 hover:bg-orange-500/20 disabled:opacity-50"
            >
              {stopping ? "Stopping…" : "Stop turn"}
            </button>
          </form>
        )}
        {!closed && (
          <form
            action={delAction}
            onSubmit={(e) => {
              if (!confirm("Delete this session and destroy its sandbox?")) e.preventDefault();
            }}
          >
            <input type="hidden" name="id" value={sessionId} />
            <button
              disabled={deleting}
              className="rounded border border-red-500/60 bg-red-500/10 px-3 py-1.5 text-sm text-red-200 hover:bg-red-500/20 disabled:opacity-50"
            >
              {deleting ? "Deleting…" : "Delete session"}
            </button>
          </form>
        )}
        {closed && <span className="text-xs text-muted">Session is closed.</span>}
      </div>
      <Msg state={stopState} />
      <Msg state={delState} />
    </div>
  );
}

function EgressPanel({ denied, allowedCount }: { denied: SessionEvent[]; allowedCount: number }) {
  return (
    <details className="rounded border border-line bg-panel" open>
      <summary className="flex items-center gap-2 px-3 py-2 text-sm">
        <span className="font-medium">Egress denied</span>
        <span
          className={`rounded px-1.5 font-mono text-xs ${denied.length ? "bg-red-500/20 text-red-300" : "bg-line text-muted"}`}
        >
          {denied.length}
        </span>
        <span className="ml-auto font-mono text-[11px] text-muted">{allowedCount} allowed</span>
      </summary>
      <div className="border-t border-line">
        {denied.length === 0 ? (
          <p className="px-3 py-2 text-xs text-muted">No denied requests.</p>
        ) : (
          <ul className="max-h-72 overflow-auto">
            {denied.map((e) => {
              const p = e.payload as unknown as EgressPayload;
              return (
                <li key={e.id} className="border-b border-line/60 px-3 py-1.5 font-mono text-xs last:border-0">
                  <div className="flex justify-between gap-2">
                    <span className="break-all text-red-300">{p.host}</span>
                    <span className="shrink-0 text-muted">turn {e.turn_seq ?? "–"}</span>
                  </div>
                  <div className="text-muted">
                    {p.via} {p.method} {p.path}
                  </div>
                  {p.reason && <div className="text-muted/80">{p.reason}</div>}
                </li>
              );
            })}
          </ul>
        )}
      </div>
    </details>
  );
}

function FollowUp({ sessionId, disabled, running }: { sessionId: string; disabled: boolean; running: boolean }) {
  const [state, action, pending] = useActionState(followUp, {});
  const formRef = useRef<HTMLFormElement>(null);
  useEffect(() => {
    if (state.ok) formRef.current?.reset();
  }, [state]);
  return (
    <form
      ref={formRef}
      action={action}
      className="sticky bottom-0 z-10 -mx-3 flex flex-col gap-1.5 border-t border-line bg-bg/95 px-3 py-2 backdrop-blur sm:mx-0 sm:rounded sm:border"
    >
      <input type="hidden" name="id" value={sessionId} />
      <div className="flex gap-2">
        <textarea
          name="prompt"
          required
          rows={2}
          disabled={disabled}
          placeholder={disabled ? "Session is closed" : "Follow-up prompt…"}
          onKeyDown={(e) => {
            if (e.key === "Enter" && (e.metaKey || e.ctrlKey)) e.currentTarget.form?.requestSubmit();
          }}
          className="min-w-0 flex-1 resize-y rounded border border-line bg-panel px-2 py-1.5 text-sm outline-none focus:border-accent disabled:opacity-50"
        />
        <button
          disabled={disabled || pending}
          className="self-end rounded bg-accent px-3 py-1.5 text-sm font-medium text-black disabled:opacity-40"
        >
          {pending ? "…" : running ? "Queue" : "Send"}
        </button>
      </div>
      <div className="flex gap-3">
        {running && !disabled && <p className="text-xs text-sky-300">A turn is running; this prompt will be queued.</p>}
        <Msg state={state} />
      </div>
    </form>
  );
}
