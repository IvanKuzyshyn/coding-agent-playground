import { formatTokens } from "@/lib/format";

export function TokenBar({ used, budget, wide }: { used: number; budget: number; wide?: boolean }) {
  const pct = budget > 0 ? Math.min(100, (used / budget) * 100) : 0;
  const color = pct >= 90 ? "bg-red-500" : pct >= 70 ? "bg-amber-500" : "bg-sky-500";
  return (
    <div className={`flex items-center gap-2 ${wide ? "w-full" : ""}`}>
      <div className={`h-1.5 overflow-hidden rounded bg-line ${wide ? "flex-1" : "w-16"}`}>
        <div className={`h-full ${color}`} style={{ width: `${pct}%` }} />
      </div>
      <span className="font-mono text-xs whitespace-nowrap text-muted">
        {formatTokens(used)} / {formatTokens(budget)}
      </span>
    </div>
  );
}
