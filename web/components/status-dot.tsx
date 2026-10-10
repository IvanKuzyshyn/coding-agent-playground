import { capitalize } from "@/lib/format";

// Session display statuses and turn statuses share one palette.
const DOT: Record<string, string> = {
  pending: "bg-slate-400",
  provisioning: "bg-amber-400",
  idle: "bg-violet-400",
  running: "bg-sky-400 animate-pulse",
  stopping: "bg-orange-400",
  stopped: "bg-zinc-500",
  failed: "bg-red-500",
  queued: "bg-slate-400",
  complete: "bg-emerald-400",
  limit_exceeded: "bg-amber-400",
};

const TEXT: Record<string, string> = {
  pending: "text-slate-300",
  provisioning: "text-amber-300",
  idle: "text-violet-300",
  running: "text-sky-300",
  stopping: "text-orange-300",
  stopped: "text-zinc-400",
  failed: "text-red-300",
  queued: "text-slate-300",
  complete: "text-emerald-300",
  limit_exceeded: "text-amber-300",
};

export function StatusDot({ status, className = "" }: { status: string; className?: string }) {
  return (
    <span className={`inline-flex items-center gap-1.5 whitespace-nowrap ${TEXT[status] ?? "text-fg"} ${className}`}>
      <span aria-hidden className={`h-1.5 w-1.5 shrink-0 rounded-full ${DOT[status] ?? "bg-zinc-400"}`} />
      {capitalize(status.replace("_", " "))}
    </span>
  );
}
