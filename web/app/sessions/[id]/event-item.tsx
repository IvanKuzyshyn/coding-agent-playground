import type { ReactNode } from "react";
import { formatTokens } from "@/lib/format";
import type { SessionEvent } from "@/lib/types";

type Obj = Record<string, unknown>;

const obj = (v: unknown): Obj => (v && typeof v === "object" && !Array.isArray(v) ? (v as Obj) : {});
const str = (v: unknown): string => (typeof v === "string" ? v : v == null ? "" : String(v));
const num = (v: unknown): number | undefined => (typeof v === "number" ? v : undefined);
const clip = (s: string, n: number) => (s.length > n ? `${s.slice(0, n)}…` : s);

function time(ts: string) {
  const d = new Date(ts);
  return isNaN(d.getTime()) ? "" : d.toLocaleTimeString([], { hour12: false });
}

function Row({ ev, tag, tagClass, children }: { ev: SessionEvent; tag: string; tagClass?: string; children: ReactNode }) {
  return (
    <div className="flex gap-2 py-0.5 text-[13px] leading-5">
      <span className="hidden w-16 shrink-0 font-mono text-[11px] leading-5 text-muted/70 sm:inline" title={`#${ev.id} ${ev.ts}`}>
        {time(ev.ts)}
      </span>
      <span className={`w-16 shrink-0 font-mono text-[11px] leading-5 ${tagClass ?? "text-muted"}`}>{tag}</span>
      <div className="min-w-0 flex-1">{children}</div>
    </div>
  );
}

function Collapsed({ summary, body, error }: { summary: ReactNode; body: string; error?: boolean }) {
  return (
    <details className="min-w-0">
      <summary className={`truncate font-mono text-xs ${error ? "text-red-300" : "text-muted"}`}>{summary}</summary>
      <pre className="mt-1 max-h-80 overflow-auto rounded border border-line bg-bg p-2 font-mono text-xs whitespace-pre-wrap break-words text-fg/90">
        {body}
      </pre>
    </details>
  );
}

function Json({ value }: { value: unknown }) {
  return <>{JSON.stringify(value, null, 2)}</>;
}

function Unknown({ ev, label }: { ev: SessionEvent; label?: string }) {
  return (
    <Row ev={ev} tag={ev.kind}>
      <Collapsed summary={label ?? ev.kind} body={JSON.stringify(ev.payload, null, 2)} />
    </Row>
  );
}

function Text({ children }: { children: string }) {
  return <p className="whitespace-pre-wrap break-words text-fg">{children}</p>;
}

// Normalised like the API's turn usage so the event and the turn header agree: "in" is uncached
// input (Claude reports cache writes separately; Codex includes cache reads in input_tokens).
function usageLine(u: Obj, harness: "claude-code" | "codex"): string {
  const parts: string[] = [];
  const raw = num(u.input_tokens);
  const outT = num(u.output_tokens);
  const cached = harness === "codex" ? num(u.cached_input_tokens) : num(u.cache_read_input_tokens);
  const inT =
    raw === undefined
      ? undefined
      : harness === "codex"
        ? raw - (cached ?? 0)
        : raw + (num(u.cache_creation_input_tokens) ?? 0);
  if (inT !== undefined) parts.push(`in ${formatTokens(inT)}`);
  if (cached !== undefined) parts.push(`cached ${formatTokens(cached)}`);
  if (outT !== undefined) parts.push(`out ${formatTokens(outT)}`);
  return parts.join(" · ");
}

function compactInput(input: Obj): string {
  const pick = input.command ?? input.file_path ?? input.path ?? input.pattern ?? input.url ?? input.query;
  if (typeof pick === "string") return pick;
  return JSON.stringify(input);
}

function toolResultText(content: unknown): string {
  if (typeof content === "string") return content;
  if (Array.isArray(content)) {
    return content.map((c) => (obj(c).type === "text" ? str(obj(c).text) : JSON.stringify(c))).join("\n");
  }
  return JSON.stringify(content, null, 2);
}

function ClaudeEvent({ ev, e, hideResultText }: { ev: SessionEvent; e: Obj; hideResultText: boolean }) {
  const type = str(e.type);
  if (type === "system") {
    const sub = str(e.subtype);
    const detail = sub === "init" ? `model=${str(e.model)} session=${str(e.session_id).slice(0, 8)}` : "";
    return (
      <Row ev={ev} tag="claude">
        <Collapsed summary={`system/${sub} ${detail}`} body={JSON.stringify(e, null, 2)} />
      </Row>
    );
  }
  if (type === "assistant" || type === "user") {
    const content = obj(e.message).content;
    const blocks = Array.isArray(content) ? content.map(obj) : [];
    if (typeof content === "string") blocks.push({ type: "text", text: content });
    return (
      <>
        {blocks.map((b, i) => {
          if (b.type === "text") {
            return (
              <Row key={i} ev={ev} tag={type === "assistant" ? "assistant" : "user"} tagClass="text-violet-300">
                <Text>{str(b.text)}</Text>
              </Row>
            );
          }
          if (b.type === "thinking") {
            return (
              <Row key={i} ev={ev} tag="thinking">
                <Collapsed summary={clip(str(b.thinking), 100) || "thinking"} body={str(b.thinking)} />
              </Row>
            );
          }
          if (b.type === "tool_use") {
            const input = obj(b.input);
            return (
              <Row key={i} ev={ev} tag="tool" tagClass="text-amber-300">
                <div className="flex min-w-0 gap-2 font-mono text-xs">
                  <span className="shrink-0 text-amber-200">{str(b.name)}</span>
                  <span className="truncate text-fg/80" title={JSON.stringify(input)}>
                    {clip(compactInput(input), 240)}
                  </span>
                </div>
              </Row>
            );
          }
          if (b.type === "tool_result") {
            const text = toolResultText(b.content);
            const isErr = b.is_error === true;
            return (
              <Row key={i} ev={ev} tag="result" tagClass={isErr ? "text-red-300" : "text-muted"}>
                <Collapsed summary={clip(text.split("\n")[0] || "(empty)", 120)} body={text} error={isErr} />
              </Row>
            );
          }
          return (
            <Row key={i} ev={ev} tag={type}>
              <Collapsed summary={str(b.type) || "block"} body={JSON.stringify(b, null, 2)} />
            </Row>
          );
        })}
      </>
    );
  }
  if (type === "result") {
    const isErr = e.is_error === true || str(e.subtype) !== "success";
    const cost = num(e.total_cost_usd);
    return (
      <Row ev={ev} tag="done" tagClass={isErr ? "text-red-300" : "text-emerald-300"}>
        <div className="rounded border border-line bg-panel2 px-2 py-1.5">
          <div className="mb-1 flex flex-wrap gap-x-3 font-mono text-[11px] text-muted">
            <span className={isErr ? "text-red-300" : "text-emerald-300"}>{str(e.subtype) || "result"}</span>
            {num(e.num_turns) !== undefined && <span>{num(e.num_turns)} steps</span>}
            <span>{usageLine(obj(e.usage), "claude-code")}</span>
            {cost !== undefined && <span title="Client-side estimate; not billed on a subscription">${cost.toFixed(3)} API-equiv</span>}
          </div>
          {str(e.result) && (isErr || !hideResultText) && <Text>{str(e.result)}</Text>}
        </div>
      </Row>
    );
  }
  if (type === "rate_limit_event") {
    // Emitted on every request; only worth showing once a limit actually bites.
    const info = obj(e.rate_limit_info);
    const status = str(info.status);
    if (!status || status === "allowed") return null;
    const resets = num(info.resetsAt);
    return (
      <Row ev={ev} tag="limit" tagClass="text-amber-300">
        <p className="rounded border border-amber-500/40 bg-amber-500/10 px-2 py-1 font-mono text-xs text-amber-200">
          Rate limit {status === "rejected" ? "hit" : "warning"}
          {info.rateLimitType ? ` (${str(info.rateLimitType)})` : ""}
          {resets !== undefined ? ` · resets ${time(new Date(resets * 1000).toISOString())}` : ""}
          <span className="text-amber-200/60"> · {status}</span>
        </p>
      </Row>
    );
  }
  return <Unknown ev={ev} label={`claude ${type || "event"}`} />;
}

function CodexEvent({ ev, e, completedItems }: { ev: SessionEvent; e: Obj; completedItems: Set<string> }) {
  const type = str(e.type);
  const item = obj(e.item);
  const itemType = str(item.type);

  if (type === "thread.started" || type === "turn.started") {
    return (
      <Row ev={ev} tag="codex">
        <span className="font-mono text-xs text-muted">
          {type}
          {e.thread_id ? ` ${str(e.thread_id).slice(0, 8)}` : ""}
        </span>
      </Row>
    );
  }
  if (type === "turn.completed") {
    return (
      <Row ev={ev} tag="done" tagClass="text-emerald-300">
        <span className="font-mono text-xs text-muted">turn.completed · {usageLine(obj(e.usage), "codex")}</span>
      </Row>
    );
  }
  if (type === "turn.failed" || type === "error") {
    const msg = str(obj(e.error).message) || str(e.message) || JSON.stringify(e);
    return (
      <Row ev={ev} tag="error" tagClass="text-red-300">
        <p className="font-mono text-xs break-words text-red-300">
          {type}: {msg}
        </p>
      </Row>
    );
  }
  if (type === "item.started" && itemType === "command_execution") {
    if (completedItems.has(str(item.id))) return null;
    return (
      <Row ev={ev} tag="exec" tagClass="text-amber-300">
        <span className="font-mono text-xs text-fg/80">
          $ {str(item.command)} <span className="text-sky-300">running…</span>
        </span>
      </Row>
    );
  }
  if (type === "item.started" || type === "item.updated") return null;
  if (type === "item.completed") {
    if (itemType === "agent_message") {
      return (
        <Row ev={ev} tag="agent" tagClass="text-violet-300">
          <Text>{str(item.text)}</Text>
        </Row>
      );
    }
    if (itemType === "reasoning") {
      return (
        <Row ev={ev} tag="thinking">
          <Collapsed summary={clip(str(item.text), 100) || "reasoning"} body={str(item.text)} />
        </Row>
      );
    }
    if (itemType === "command_execution") {
      const code = num(item.exit_code);
      const ok = code === 0;
      return (
        <Row ev={ev} tag="exec" tagClass="text-amber-300">
          <div className="flex min-w-0 flex-col">
            <div className="flex min-w-0 gap-2 font-mono text-xs">
              <span className="truncate text-fg/80" title={str(item.command)}>
                $ {str(item.command)}
              </span>
              <span className={`shrink-0 ${ok ? "text-emerald-300" : "text-red-300"}`}>exit {code ?? "?"}</span>
            </div>
            {str(item.aggregated_output) && (
              <Collapsed summary="output" body={str(item.aggregated_output)} error={!ok} />
            )}
          </div>
        </Row>
      );
    }
    if (itemType === "file_change") {
      const changes = Array.isArray(item.changes) ? item.changes.map(obj) : [];
      return (
        <Row ev={ev} tag="files" tagClass="text-cyan-300">
          <ul className="font-mono text-xs">
            {changes.map((c, i) => (
              <li key={i} className="break-all">
                <span className="text-muted">{str(c.kind)}</span> {str(c.path)}
              </li>
            ))}
          </ul>
        </Row>
      );
    }
  }
  return <Unknown ev={ev} label={`codex ${type}${itemType ? ` ${itemType}` : ""}`} />;
}

// Reason codes set by the gateway (internal/gateway) on denied egress.
const EGRESS_REASONS: Record<string, string> = {
  host: "host not allowlisted",
  port: "port not allowed",
  method: "method not allowed",
  status: "session not active",
  route: "unknown route",
  harness: "route not allowed for this harness",
  budget: "budget exhausted",
};

export function egressReason(reason: unknown): string {
  const r = str(reason);
  return EGRESS_REASONS[r] ?? r;
}

export function EventItem({
  ev,
  completedItems,
  hideResultText = false,
}: {
  ev: SessionEvent;
  completedItems: Set<string>;
  hideResultText?: boolean;
}) {
  const p = obj(ev.payload);
  switch (ev.kind) {
    case "harness": {
      const e = obj(p.event);
      if (p.harness === "codex") return <CodexEvent ev={ev} e={e} completedItems={completedItems} />;
      if (p.harness === "claude-code") return <ClaudeEvent ev={ev} e={e} hideResultText={hideResultText} />;
      return <Unknown ev={ev} />;
    }
    case "lifecycle": {
      const text = p.turn_status
        ? `turn → ${str(p.turn_status)}`
        : `session → ${str(p.status)}${p.message ? ` (${str(p.message)})` : ""}`;
      return (
        <Row ev={ev} tag="state">
          <span className="font-mono text-xs text-muted">{text}</span>
        </Row>
      );
    }
    case "system":
      return (
        <Row ev={ev} tag="system">
          <span className="font-mono text-xs text-muted">{str(p.message) || <Json value={p} />}</span>
        </Row>
      );
    case "stderr":
      return (
        <Row ev={ev} tag="stderr" tagClass="text-orange-300/80">
          <span className="font-mono text-xs break-all whitespace-pre-wrap text-orange-200/70">{str(p.line)}</span>
        </Row>
      );
    case "egress": {
      const denied = p.decision === "denied";
      return (
        <Row ev={ev} tag="egress" tagClass={denied ? "text-red-400" : "text-muted"}>
          <span className={`font-mono text-xs break-all ${denied ? "text-red-300" : "text-muted"}`}>
            {str(p.decision)} {str(p.method)} {str(p.host)}
            {str(p.path)} {p.reason ? `(${egressReason(p.reason)})` : ""}
          </span>
        </Row>
      );
    }
    default:
      return <Unknown ev={ev} />;
  }
}
