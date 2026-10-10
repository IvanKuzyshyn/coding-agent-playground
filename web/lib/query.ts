// The runs view state lives in the URL query string; this maps it to and from the API's listing
// parameters. Unknown values are dropped rather than forwarded, because the API rejects them.
import { DISPLAY_STATUSES, HARNESSES, type DisplayStatus, type Harness } from "./types";

export const PAGE_SIZE = 50;

export const SORTS = ["created_at", "duration", "cost", "tokens", "name", "status"] as const;
export type SortKey = (typeof SORTS)[number];
export type Order = "asc" | "desc";

export const SORT_LABELS: Record<SortKey, string> = {
  created_at: "When",
  duration: "Duration",
  cost: "Cost",
  tokens: "Tokens",
  name: "Name",
  status: "Status",
};

// The direction a column sorts in when first selected.
export const DEFAULT_ORDER: Record<SortKey, Order> = {
  created_at: "desc",
  duration: "desc",
  cost: "desc",
  tokens: "desc",
  name: "asc",
  status: "asc",
};

export interface RunsQuery {
  q: string;
  status: DisplayStatus[];
  harness: Harness[];
  sort: SortKey;
  order: Order;
  page: number;
  session: string | null;
}

type Params = Record<string, string | string[] | undefined>;

function first(v: string | string[] | undefined): string {
  return (Array.isArray(v) ? v[0] : v) ?? "";
}

function csv<T extends string>(raw: string, allowed: readonly T[]): T[] {
  const set = new Set(raw.split(","));
  // Keep a canonical order so equal filters produce equal URLs.
  return allowed.filter((v) => set.has(v));
}

export function parseQuery(params: Params | URLSearchParams): RunsQuery {
  const get = (k: string) => (params instanceof URLSearchParams ? (params.get(k) ?? "") : first(params[k]));
  const sort = get("sort") as SortKey;
  const order = get("order");
  const page = Number(get("page"));
  return {
    q: get("q").trim(),
    status: csv(get("status"), DISPLAY_STATUSES),
    harness: csv(get("harness"), HARNESSES),
    sort: SORTS.includes(sort) ? sort : "created_at",
    order: order === "asc" || order === "desc" ? order : "desc",
    page: Number.isInteger(page) && page > 1 ? page : 1,
    session: get("session") || null,
  };
}

// Query string for the UI URL; defaults are omitted to keep links short.
export function uiSearch(query: RunsQuery): string {
  const p = new URLSearchParams();
  if (query.q) p.set("q", query.q);
  if (query.status.length) p.set("status", query.status.join(","));
  if (query.harness.length) p.set("harness", query.harness.join(","));
  if (query.sort !== "created_at") p.set("sort", query.sort);
  if (query.order !== "desc") p.set("order", query.order);
  if (query.page > 1) p.set("page", String(query.page));
  if (query.session) p.set("session", query.session);
  const s = p.toString();
  return s ? `?${s}` : "";
}

export function uiHref(query: RunsQuery): string {
  return `/${uiSearch(query)}`;
}

export function apiSearch(query: RunsQuery): string {
  const p = new URLSearchParams();
  if (query.q) p.set("q", query.q);
  if (query.status.length) p.set("status", query.status.join(","));
  if (query.harness.length) p.set("harness", query.harness.join(","));
  p.set("sort", query.sort);
  p.set("order", query.order);
  p.set("limit", String(PAGE_SIZE));
  p.set("offset", String((query.page - 1) * PAGE_SIZE));
  return p.toString();
}
