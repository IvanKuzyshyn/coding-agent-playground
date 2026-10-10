const COLORS: Record<string, string> = {
  pending: "text-slate-300 border-slate-500/50 bg-slate-500/10",
  provisioning: "text-amber-300 border-amber-500/50 bg-amber-500/10",
  queued: "text-slate-300 border-slate-500/50 bg-slate-500/10",
  idle: "text-emerald-300 border-emerald-500/50 bg-emerald-500/10",
  complete: "text-emerald-300 border-emerald-500/50 bg-emerald-500/10",
  running: "text-sky-300 border-sky-500/60 bg-sky-500/15",
  stopping: "text-orange-300 border-orange-500/50 bg-orange-500/10",
  stopped: "text-zinc-400 border-zinc-600/60 bg-zinc-600/10",
  failed: "text-red-300 border-red-500/60 bg-red-500/15",
  limit_exceeded: "text-red-300 border-red-500/60 bg-red-500/15",
};

export function StatusBadge({ status }: { status: string }) {
  const cls = COLORS[status] ?? "text-zinc-300 border-zinc-500/50";
  return (
    <span
      className={`inline-flex items-center gap-1 rounded border px-1.5 py-px font-mono text-[11px] leading-4 whitespace-nowrap ${cls}`}
    >
      {status === "running" && <span className="h-1.5 w-1.5 animate-pulse rounded-full bg-sky-400" />}
      {status}
    </span>
  );
}
