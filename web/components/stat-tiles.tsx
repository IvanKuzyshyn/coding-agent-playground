import type { ReactNode } from "react";
import { formatPercent } from "@/lib/format";
import type { Stats } from "@/lib/types";

export function StatTiles({ stats }: { stats: Stats }) {
  return (
    <section
      aria-label="Run statistics"
      className="grid grid-cols-1 divide-y divide-line overflow-hidden rounded-lg border border-line bg-panel sm:grid-cols-3 sm:divide-x sm:divide-y-0"
    >
      <Tile
        label="Total runs"
        value={stats.total.toLocaleString("en-US")}
        sub="all time"
        icon={<path d="M3 4h10M3 8h10M3 12h6" />}
        tone="text-sky-300"
      />
      <Tile
        label="Failure rate"
        value={formatPercent(stats.failure_rate)}
        sub={`${stats.failed} failed`}
        icon={<path d="M8 2.5 14 13H2Z M8 6.5v3 M8 11.2v.1" />}
        tone="text-red-300"
      />
      <Tile
        label="Running now"
        value={String(stats.running)}
        sub={`${stats.active} active`}
        icon={<path d="M1.5 8.5h3l1.5-4 3 7 1.5-3h4" />}
        tone="text-amber-300"
      />
    </section>
  );
}

function Tile({ label, value, sub, icon, tone }: { label: string; value: string; sub: string; icon: ReactNode; tone: string }) {
  return (
    <div className="flex items-center gap-3 px-4 py-3.5">
      <span className={`flex h-8 w-8 items-center justify-center rounded-md bg-panel2 ${tone}`} aria-hidden>
        <svg viewBox="0 0 16 16" width="16" height="16" fill="none" stroke="currentColor" strokeWidth="1.3" strokeLinecap="round" strokeLinejoin="round">
          {icon}
        </svg>
      </span>
      <div className="min-w-0">
        <div className="text-[11px] font-medium tracking-widest text-muted uppercase">{label}</div>
        <div className="flex items-baseline gap-2">
          <span className="text-xl font-semibold tabular-nums text-fg" data-stat={label}>
            {value}
          </span>
          <span className="text-xs text-muted">{sub}</span>
        </div>
      </div>
    </div>
  );
}
