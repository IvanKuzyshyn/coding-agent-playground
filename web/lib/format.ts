export function formatTokens(n: number): string {
  if (n >= 1_000_000) return `${(n / 1_000_000).toFixed(n >= 10_000_000 ? 0 : 1)}M`;
  if (n >= 1_000) return `${(n / 1_000).toFixed(n >= 10_000 ? 0 : 1)}k`;
  return String(n);
}

// Signed relative time: "3m ago" for the past, "in 2h" for the future.
export function relTime(iso: string, now: number): string {
  const diff = new Date(iso).getTime() - now;
  const abs = Math.abs(diff) / 1000;
  let v: string;
  if (abs < 60) v = `${Math.floor(abs)}s`;
  else if (abs < 3600) v = `${Math.floor(abs / 60)}m`;
  else if (abs < 86400) v = `${Math.floor(abs / 3600)}h${Math.floor((abs % 3600) / 60)}m`;
  else v = `${Math.floor(abs / 86400)}d`;
  return diff <= 0 ? `${v} ago` : `in ${v}`;
}

export function shortId(id: string): string {
  return id.slice(0, 8);
}

// "4m 40s", "2h 5m", "3d 4h".
export function formatDuration(totalSeconds: number): string {
  const s = Math.max(0, Math.floor(totalSeconds));
  if (s < 60) return `${s}s`;
  if (s < 3600) return `${Math.floor(s / 60)}m ${s % 60}s`;
  if (s < 86400) return `${Math.floor(s / 3600)}h ${Math.floor((s % 3600) / 60)}m`;
  return `${Math.floor(s / 86400)}d ${Math.floor((s % 86400) / 3600)}h`;
}

export function formatUsd(n: number): string {
  return `$${n > 0 && n < 0.01 ? n.toFixed(3) : n.toFixed(2)}`;
}

export function formatPercent(ratio: number): string {
  const pct = ratio * 100;
  return `${pct >= 10 || pct === 0 ? pct.toFixed(0) : pct.toFixed(1)}%`;
}

export function capitalize(s: string): string {
  return s.charAt(0).toUpperCase() + s.slice(1);
}

// "Oct 9 10:13" in the viewer's time zone.
export function absTime(iso: string): string {
  const d = new Date(iso);
  if (isNaN(d.getTime())) return "";
  const date = d.toLocaleDateString("en-US", { month: "short", day: "numeric" });
  const time = d.toLocaleTimeString("en-GB", { hour: "2-digit", minute: "2-digit", hour12: false });
  return `${date} ${time}`;
}
