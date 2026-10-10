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
