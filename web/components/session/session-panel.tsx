"use client";

import { useRouter } from "next/navigation";
import { useActionState, useEffect, useMemo, useRef, useState, type ReactNode, type RefObject } from "react";
import { deleteSession, followUp, stopTurn, type ActionState } from "@/app/actions";
import { costLabel, costTitle } from "@/components/cost";
import { HARNESS_LABELS, HarnessIcon } from "@/components/harness-icon";
import { LiveDuration, LocalTime } from "@/components/live";
import { RelTime } from "@/components/rel-time";
import { StatusDot } from "@/components/status-dot";
import { TokenBar } from "@/components/token-bar";
import { formatDuration, formatTokens, shortId } from "@/lib/format";
import { TERMINAL_SESSION, type EgressPayload, type Session, type SessionEvent, type Turn } from "@/lib/types";
import { EventItem, egressReason } from "./event-item";
import { CloseButton } from "./close-button";

const KINDS = ["lifecycle", "harness", "stderr", "egress", "system", "message"];
type Conn = "connecting" | "live" | "reconnecting" | "ended";
type Tab = "timeline" | "turns" | "egress" | "details";

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

export function SessionPanel({ session, onClose }: { session: Session; onClose: () => void }) {
  const { events, conn } = useEventStream(session.id, session.status === "stopped");
  const [tab, setTab] = useState<Tab>("timeline");
  const scrollRef = useRef<HTMLDivElement>(null);
  const denied = events.filter((e) => e.kind === "egress" && e.payload.decision === "denied");
  const allowed = events.filter((e) => e.kind === "egress" && e.payload.decision === "allowed");

  useFollowTail(scrollRef, tab === "timeline" ? events.length : 0);

  const running = session.status === "running";
  const closed = TERMINAL_SESSION.includes(session.status);

  const tabs: { id: Tab; label: string; badge?: ReactNode }[] = [
    { id: "timeline", label: "Timeline", badge: <Count n={events.length} /> },
    { id: "turns", label: "Turns", badge: <Count n={session.turn_count} /> },
    { id: "egress", label: "Egress", badge: <Count n={denied.length} alert={denied.length > 0} label="denied" /> },
    { id: "details", label: "Details" },
  ];

  return (
    <div className="flex h-full flex-col">
      <Header session={session} running={running} onClose={onClose} />

      <div role="tablist" aria-label="Session views" className="flex shrink-0 gap-1 overflow-x-auto border-b border-line px-3 sm:px-5">
        {tabs.map((t) => (
          <button
            key={t.id}
            type="button"
            role="tab"
            id={`tab-${t.id}`}
            aria-selected={tab === t.id}
            aria-controls={`panel-${t.id}`}
            onClick={() => setTab(t.id)}
            className={`-mb-px flex items-center gap-1.5 border-b-2 px-2.5 py-2.5 text-sm whitespace-nowrap ${
              tab === t.id ? "border-accent text-fg" : "border-transparent text-muted hover:text-fg"
            }`}
          >
            {t.label}
            {t.badge}
          </button>
        ))}
        <span className="ml-auto self-center pl-3 font-mono text-[11px] whitespace-nowrap text-muted">
          stream <ConnDot conn={conn} />
        </span>
      </div>

      <div
        ref={scrollRef}
        role="tabpanel"
        id={`panel-${tab}`}
        aria-labelledby={`tab-${tab}`}
        className="min-h-0 flex-1 overflow-y-auto overscroll-contain px-3 py-3 sm:px-5"
      >
        {tab === "timeline" && <Timeline session={session} events={events} />}
        {tab === "turns" && <Turns turns={session.turns ?? []} />}
        {tab === "egress" && <EgressPanel denied={denied} allowed={allowed} />}
        {tab === "details" && <Details session={session} />}
      </div>

      <FollowUp
        sessionId={session.id}
        disabled={closed}
        running={running || session.current_turn !== undefined}
        stateKey={`${session.status}|${session.current_turn ?? ""}`}
      />
    </div>
  );
}

function Count({ n, alert, label }: { n: number; alert?: boolean; label?: string }) {
  return (
    <span
      aria-label={label ? `${n} ${label}` : undefined}
      className={`rounded px-1.5 font-mono text-[11px] ${alert ? "bg-red-500/20 text-red-300" : "bg-panel2 text-muted"}`}
    >
      {n}
    </span>
  );
}

function Header({ session, running, onClose }: { session: Session; running: boolean; onClose: () => void }) {
  const live = !TERMINAL_SESSION.includes(session.status);
  return (
    <header className="flex shrink-0 flex-col gap-3 border-b border-line px-3 pt-3 pb-3 sm:px-5">
      <div className="flex items-center gap-2">
        <span className="rounded-md border border-line bg-panel px-2 py-0.5 text-xs">
          <StatusDot status={session.display_status} />
        </span>
        <span className="truncate font-mono text-xs text-muted" title={session.id}>
          <span className="sm:hidden">{shortId(session.id)}</span>
          <span className="hidden sm:inline">{session.id}</span>
        </span>
        <div className="ml-auto flex items-center gap-2">
          <Controls sessionId={session.id} status={session.status} running={running} />
          <CloseButton onClose={onClose} />
        </div>
      </div>
      <h2 className="text-lg leading-snug font-semibold break-words text-fg">{session.name}</h2>
      <dl className="flex flex-wrap items-center gap-x-0 gap-y-1.5 text-xs">
        <Meta label="Harness">
          <span className="inline-flex items-center gap-1.5">
            <HarnessIcon harness={session.harness} size={18} />
            {HARNESS_LABELS[session.harness]}
          </span>
        </Meta>
        <Meta label="Repo">{session.repo ?? "–"}</Meta>
        <Meta label="Preset">{session.preset}</Meta>
        <Meta label="Model">{session.model ?? "harness default"}</Meta>
        <Meta label="Duration">
          <LiveDuration session={session} />
        </Meta>
        <Meta label="Cost">
          <span title={costTitle(session.cost_complete)} className={session.cost_complete ? "" : "text-amber-300"}>
            {costLabel(session)}
          </span>
        </Meta>
        <Meta label="Created">
          <RelTime iso={session.created_at} />
        </Meta>
        <Meta label="Expires">{live ? <RelTime iso={session.expires_at} /> : "–"}</Meta>
        <Meta label="Idle until">{live && session.idle_until ? <RelTime iso={session.idle_until} /> : "–"}</Meta>
        {session.ended_at && (
          <Meta label="Ended">
            <RelTime iso={session.ended_at} />
          </Meta>
        )}
      </dl>
      <div className="flex items-center gap-2">
        <span className="text-xs text-muted">Tokens</span>
        <TokenBar used={session.tokens_used} budget={session.token_budget} wide />
      </div>
      {session.error && (
        <p className="rounded border border-red-500/40 bg-red-500/10 px-2 py-1 font-mono text-xs text-red-300">{session.error}</p>
      )}
    </header>
  );
}

function Meta({ label, children }: { label: string; children: ReactNode }) {
  return (
    <div className="flex items-center gap-1.5 border-line pr-3 not-first:border-l not-first:pl-3">
      <dt className="text-muted">{label}</dt>
      <dd className="font-medium text-fg">{children}</dd>
    </div>
  );
}

function Timeline({ session, events }: { session: Session; events: SessionEvent[] }) {
  const turns = useMemo(() => groupTurns(session.turns ?? [], events), [session.turns, events]);
  const completedItems = useMemo(() => {
    const s = new Set<string>();
    for (const e of events) {
      const ev = (e.payload.event ?? {}) as { type?: string; item?: { id?: string } };
      if (e.kind === "harness" && ev.type === "item.completed" && ev.item?.id) s.add(ev.item.id);
    }
    return s;
  }, [events]);
  const repeatedResults = useMemo(() => findRepeatedResults(events), [events]);
  const sessionEvents = events.filter((e) => !e.turn_seq);

  return (
    <div className="flex flex-col gap-3">
      {sessionEvents.length > 0 && (
        <details className="rounded-lg border border-line bg-panel">
          <summary className="px-3 py-1.5 font-mono text-xs text-muted">session events ({sessionEvents.length})</summary>
          <div className="border-t border-line px-2 py-1">
            {sessionEvents.map((ev) => (
              <EventItem key={ev.id} ev={ev} completedItems={completedItems} />
            ))}
          </div>
        </details>
      )}

      {turns.map(({ seq, turn, events: tEvents }) => (
        <section key={seq} className="rounded-lg border border-line bg-panel">
          <div className="flex flex-wrap items-center gap-2 border-b border-line px-3 py-1.5">
            <span className="font-mono text-xs text-fg">Turn {seq}</span>
            {turn && <StatusDot status={turn.status} className="text-xs" />}
            {turn?.usage && <UsageLine usage={turn.usage} />}
          </div>
          {turn && (
            <p className="border-b border-line bg-panel2 px-3 py-2 text-sm break-words whitespace-pre-wrap">
              <span className="mr-2 font-mono text-xs text-accent">prompt</span>
              {turn.prompt}
            </p>
          )}
          <div className="px-2 py-1">
            {tEvents.length === 0 ? (
              <p className="px-1 py-1 font-mono text-xs text-muted">no events yet</p>
            ) : (
              tEvents.map((ev) => (
                <EventItem key={ev.id} ev={ev} completedItems={completedItems} hideResultText={repeatedResults.has(ev.id)} />
              ))
            )}
          </div>
          {turn?.error && (
            <p className={`border-t border-line px-3 py-1.5 font-mono text-xs ${TURN_ERROR_CLASS[turn.status] ?? "text-muted"}`}>
              {turn.error}
            </p>
          )}
        </section>
      ))}
      {turns.length === 0 && sessionEvents.length === 0 && <p className="text-sm text-muted">No events yet.</p>}
    </div>
  );
}

function UsageLine({ usage }: { usage: NonNullable<Turn["usage"]> }) {
  if (usage.input_tokens === 0 && usage.output_tokens === 0) return null;
  return (
    <span className="font-mono text-[11px] text-muted">
      in {formatTokens(usage.input_tokens)} · cached {formatTokens(usage.cached_input_tokens)} · out{" "}
      {formatTokens(usage.output_tokens)}
      {stepsLabel(usage.agent_steps)}
    </span>
  );
}

function turnSeconds(t: Turn): number | null {
  if (!t.started_at) return null;
  const end = t.ended_at ? new Date(t.ended_at).getTime() : Date.now();
  return (end - new Date(t.started_at).getTime()) / 1000;
}

function Turns({ turns }: { turns: Turn[] }) {
  if (turns.length === 0) return <p className="text-sm text-muted">No turns yet.</p>;
  return (
    <ul className="flex flex-col gap-2">
      {turns.map((t) => {
        const secs = t.ended_at ? turnSeconds(t) : null;
        return (
          <li key={t.seq}>
            <details className="group rounded-lg border border-line bg-panel">
              <summary className="flex items-center gap-3 px-3 py-2.5 text-sm">
                <span className="w-14 shrink-0 font-mono text-xs text-muted">Turn {t.seq}</span>
                <StatusDot status={t.status} className="w-28 shrink-0 text-xs" />
                <span className="min-w-0 flex-1 truncate text-fg">{t.prompt}</span>
                <span className="hidden shrink-0 font-mono text-[11px] text-muted sm:inline">
                  {t.usage ? `${formatTokens(t.usage.input_tokens + t.usage.output_tokens)} tok${stepsLabel(t.usage.agent_steps)}` : ""}
                  {secs !== null ? ` · ${formatDuration(secs)}` : ""}
                </span>
              </summary>
              <div className="flex flex-col gap-2 border-t border-line px-3 py-2 text-sm">
                <p className="break-words whitespace-pre-wrap">
                  <span className="mr-2 font-mono text-xs text-accent">prompt</span>
                  {t.prompt}
                </p>
                {t.result && (
                  <p className="break-words whitespace-pre-wrap">
                    <span className="mr-2 font-mono text-xs text-emerald-300">result</span>
                    {t.result}
                  </p>
                )}
                {t.error && <p className={`font-mono text-xs ${TURN_ERROR_CLASS[t.status] ?? "text-muted"}`}>{t.error}</p>}
                {t.usage && <UsageLine usage={t.usage} />}
                <p className="font-mono text-[11px] text-muted">
                  queued <LocalTime iso={t.created_at} />
                  {t.started_at && (
                    <>
                      {" "}
                      · started <LocalTime iso={t.started_at} />
                    </>
                  )}
                  {t.ended_at && (
                    <>
                      {" "}
                      · ended <LocalTime iso={t.ended_at} />
                    </>
                  )}
                </p>
              </div>
            </details>
          </li>
        );
      })}
    </ul>
  );
}

function Details({ session }: { session: Session }) {
  const rows: [string, ReactNode][] = [
    ["ID", session.id],
    ["Name", session.name],
    ["Owner", session.owner],
    ["Status", `${session.display_status}${session.display_status !== session.status ? ` (${session.status})` : ""}`],
    ["Harness", HARNESS_LABELS[session.harness]],
    ["Repo", session.repo ?? "–"],
    ["Preset", session.preset],
    ["Model", session.model ?? "harness default"],
    ["Turns", `${session.turn_count}${session.current_turn ? ` (current: ${session.current_turn})` : ""}`],
    ["Tokens", `${session.tokens_used.toLocaleString("en-US")} / ${session.token_budget.toLocaleString("en-US")}`],
    [
      "Cost",
      <span key="cost">
        {costLabel(session)} <span className="text-muted">— {costTitle(session.cost_complete)}</span>
      </span>,
    ],
    ["Duration", <LiveDuration key="dur" session={session} />],
    ["Created", <LocalTime key="c" iso={session.created_at} />],
    ["Updated", <LocalTime key="u" iso={session.updated_at} />],
    ["Expires", <LocalTime key="e" iso={session.expires_at} />],
    ["Idle until", session.idle_until ? <LocalTime key="i" iso={session.idle_until} /> : "–"],
    ["Ended", session.ended_at ? <LocalTime key="en" iso={session.ended_at} /> : "–"],
    ["Error", session.error ?? "–"],
  ];
  return (
    <dl className="grid grid-cols-[max-content_1fr] gap-x-6 gap-y-2 rounded-lg border border-line bg-panel p-4 text-sm">
      {rows.map(([k, v]) => (
        <div key={k} className="contents">
          <dt className="text-muted">{k}</dt>
          <dd className="min-w-0 font-mono text-xs leading-5 break-words text-fg">{v}</dd>
        </div>
      ))}
    </dl>
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

const TURN_ERROR_CLASS: Partial<Record<Turn["status"], string>> = {
  failed: "text-red-300",
  limit_exceeded: "text-amber-300",
};

// Claude's `result` repeats the last assistant text; return the result events whose text was
// already shown by an assistant event in the same turn.
function findRepeatedResults(events: SessionEvent[]) {
  const withText = new Set<number>();
  const repeated = new Set<number>();
  for (const e of events) {
    if (e.kind !== "harness" || e.payload.harness !== "claude-code") continue;
    const turn = e.turn_seq ?? 0;
    const ev = (e.payload.event ?? {}) as { type?: string; message?: { content?: unknown } };
    if (ev.type === "assistant") {
      const content = ev.message?.content;
      const hasText =
        typeof content === "string" ||
        (Array.isArray(content) && content.some((b) => (b as { type?: string })?.type === "text"));
      if (hasText) withText.add(turn);
    } else if (ev.type === "result" && withText.has(turn)) {
      repeated.add(e.id);
    }
  }
  return repeated;
}

// Keep the newest events in view while the engineer is already at the bottom of the drawer.
function useFollowTail(ref: RefObject<HTMLDivElement | null>, count: number) {
  const atBottom = useRef(true);
  useEffect(() => {
    const el = ref.current;
    if (!el) return;
    const onScroll = () => {
      atBottom.current = el.scrollTop + el.clientHeight >= el.scrollHeight - 120;
    };
    el.addEventListener("scroll", onScroll, { passive: true });
    return () => el.removeEventListener("scroll", onScroll);
  }, [ref]);
  useEffect(() => {
    const el = ref.current;
    if (el && count > 0 && atBottom.current) el.scrollTo({ top: el.scrollHeight });
  }, [ref, count]);
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

// An action's message describes the session as it was when the action returned; drop it once the
// session state (`stateKey`) moves on.
function useCurrentMessage(state: ActionState, stateKey: string): ActionState {
  const [pinned, setPinned] = useState({ state, stateKey });
  if (pinned.state !== state) {
    setPinned({ state, stateKey });
    return state;
  }
  return pinned.stateKey === stateKey ? state : {};
}

function Controls({ sessionId, status, running }: { sessionId: string; status: Session["status"]; running: boolean }) {
  const [stopState, stopAction, stopping] = useActionState(stopTurn, {});
  const [delState, delAction, deleting] = useActionState(deleteSession, {});
  const [last, setLast] = useState<"stop" | "delete">();
  const stateKey = `${status}|${running}`;
  const stopMsg = useCurrentMessage(stopState, stateKey);
  const delMsg = useCurrentMessage(delState, stateKey);
  const closed = TERMINAL_SESSION.includes(status);
  return (
    <div className="flex items-center gap-2">
      {!closed && (
        // "Stop requested." is moot once the turn has stopped; errors stay until the state moves on.
        <Msg state={last === "stop" ? (running || stopMsg.error ? stopMsg : {}) : last === "delete" ? delMsg : {}} />
      )}
      {running && (
        <form action={stopAction} onSubmit={() => setLast("stop")}>
          <input type="hidden" name="id" value={sessionId} />
          <button
            disabled={stopping}
            className="h-8 rounded-md border border-orange-500/60 bg-orange-500/10 px-3 text-sm whitespace-nowrap text-orange-200 hover:bg-orange-500/20 disabled:opacity-50"
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
            else setLast("delete");
          }}
        >
          <input type="hidden" name="id" value={sessionId} />
          <button
            disabled={deleting}
            className="h-8 rounded-md border border-red-500/60 bg-red-500/10 px-3 text-sm whitespace-nowrap text-red-200 hover:bg-red-500/20 disabled:opacity-50"
          >
            {deleting ? "Deleting…" : "Delete session"}
          </button>
        </form>
      )}
      {closed && (
        <span className="text-xs text-muted">{status === "stopping" ? "Session stopping…" : "Session is closed."}</span>
      )}
    </div>
  );
}

function EgressPanel({ denied, allowed }: { denied: SessionEvent[]; allowed: SessionEvent[] }) {
  return (
    <div className="flex flex-col gap-3">
      <section className="rounded-lg border border-line bg-panel">
        <h3 className="flex items-center gap-2 border-b border-line px-3 py-2 text-sm font-medium">
          Egress denied
          <span
            className={`rounded px-1.5 font-mono text-xs ${denied.length ? "bg-red-500/20 text-red-300" : "bg-line text-muted"}`}
          >
            {denied.length}
          </span>
          <span className="ml-auto font-mono text-[11px] font-normal text-muted">{allowed.length} allowed</span>
        </h3>
        {denied.length === 0 ? (
          <p className="px-3 py-2 text-xs text-muted">No denied requests.</p>
        ) : (
          <EgressList events={denied} denied />
        )}
      </section>
      {allowed.length > 0 && (
        <details className="rounded-lg border border-line bg-panel">
          <summary className="px-3 py-2 text-sm">Allowed ({allowed.length})</summary>
          <div className="border-t border-line">
            <EgressList events={allowed} />
          </div>
        </details>
      )}
    </div>
  );
}

function EgressList({ events, denied }: { events: SessionEvent[]; denied?: boolean }) {
  return (
    <ul>
      {events.map((e) => {
        const p = e.payload as unknown as EgressPayload;
        return (
          <li key={e.id} className="border-b border-line/60 px-3 py-1.5 font-mono text-xs last:border-0">
            <div className="flex justify-between gap-2">
              <span className={`break-all ${denied ? "text-red-300" : "text-fg"}`}>{p.host}</span>
              <span className="shrink-0 text-muted">turn {e.turn_seq ?? "–"}</span>
            </div>
            <div className="text-muted">
              {p.via} {p.method} {p.path}
            </div>
            {p.reason && <div className="text-muted/80">{egressReason(p.reason)}</div>}
          </li>
        );
      })}
    </ul>
  );
}

function FollowUp({
  sessionId,
  disabled,
  running,
  stateKey,
}: {
  sessionId: string;
  disabled: boolean;
  running: boolean;
  stateKey: string;
}) {
  const [state, action, pending] = useActionState(followUp, {});
  const msg = useCurrentMessage(state, stateKey);
  const formRef = useRef<HTMLFormElement>(null);
  useEffect(() => {
    if (state.ok) formRef.current?.reset();
  }, [state]);
  return (
    <form ref={formRef} action={action} className="flex shrink-0 flex-col gap-1.5 border-t border-line bg-panel px-3 py-2.5 sm:px-5">
      <input type="hidden" name="id" value={sessionId} />
      <div className="flex gap-2">
        <textarea
          name="prompt"
          required
          rows={2}
          disabled={disabled}
          aria-label="Follow-up prompt"
          placeholder={disabled ? "Session is closed" : "Follow-up prompt… (⌘/Ctrl+Enter to send)"}
          onKeyDown={(e) => {
            if (e.key === "Enter" && (e.metaKey || e.ctrlKey)) e.currentTarget.form?.requestSubmit();
          }}
          className="min-w-0 flex-1 resize-y rounded-md border border-line bg-bg px-2 py-1.5 text-sm outline-none focus:border-accent disabled:opacity-50"
        />
        <button
          disabled={disabled || pending}
          className="self-end rounded-md bg-accent px-3 py-1.5 text-sm font-medium text-black disabled:opacity-40"
        >
          {pending ? "…" : running ? "Queue" : "Send"}
        </button>
      </div>
      <div className="flex gap-3">
        {running && !disabled && <p className="text-xs text-sky-300">A turn is running; this prompt will be queued.</p>}
        {!disabled && <Msg state={msg} />}
      </div>
    </form>
  );
}

// Codex does not report agent steps (0); show steps only when the harness counted them.
function stepsLabel(steps: number): string {
  if (!steps) return "";
  return ` · ${steps} ${steps === 1 ? "step" : "steps"}`;
}
