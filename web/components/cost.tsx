import { formatTokens, formatUsd } from "@/lib/format";
import type { Session } from "@/lib/types";

const ESTIMATE_NOTE =
  "API-equivalent estimate from list prices. Subscriptions are not billed per token; this is for comparison only.";
const INCOMPLETE_NOTE = "Lower bound: some requests used models that have no price in the price table.";

export function costTitle(complete: boolean): string {
  return complete ? ESTIMATE_NOTE : `${INCOMPLETE_NOTE} ${ESTIMATE_NOTE}`;
}

export function costLabel(s: Pick<Session, "cost_usd" | "cost_complete">): string {
  return `${s.cost_complete ? "≈" : "≥"} ${formatUsd(s.cost_usd)}`;
}

// Tokens are the primary figure; the dollar value is an estimate and shown secondary.
export function Cost({ session }: { session: Pick<Session, "tokens_used" | "cost_usd" | "cost_complete"> }) {
  return (
    <span className="inline-flex flex-col leading-tight" title={costTitle(session.cost_complete)}>
      <span className="tabular-nums text-fg">{formatTokens(session.tokens_used)} tok</span>
      <span className={`text-xs tabular-nums ${session.cost_complete ? "text-muted" : "text-amber-300/90"}`}>
        {costLabel(session)}
      </span>
    </span>
  );
}
