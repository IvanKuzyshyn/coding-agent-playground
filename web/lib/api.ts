import "server-only";

import { redirect } from "next/navigation";
import { requireAuth } from "./auth";
import type { Session, SessionList, Stats, Turn } from "./types";

export const API_URL = (process.env.TERRARIUM_API_URL || "http://127.0.0.1:8080").replace(/\/$/, "");

export class ApiError extends Error {
  constructor(
    public status: number,
    message: string,
  ) {
    super(message);
  }
}

async function errorFrom(res: Response): Promise<ApiError> {
  let message = res.statusText || `HTTP ${res.status}`;
  try {
    const body = (await res.json()) as { error?: string };
    if (body.error) message = body.error;
  } catch {
    // Non-JSON error body; keep the status text.
  }
  return new ApiError(res.status, `${res.status}: ${message}`);
}

export async function apiRaw(token: string, path: string, init: RequestInit = {}): Promise<Response> {
  const headers = new Headers(init.headers);
  headers.set("Authorization", `Bearer ${token}`);
  if (init.body) headers.set("Content-Type", "application/json");
  return fetch(`${API_URL}${path}`, { ...init, headers, cache: "no-store" });
}

async function api<T>(path: string, init: RequestInit = {}): Promise<T> {
  const { token } = await requireAuth();
  const res = await apiRaw(token, path, init);
  // A revoked or rotated token: send the engineer back to log in again.
  if (res.status === 401) redirect("/login?expired=1");
  if (!res.ok) throw await errorFrom(res);
  return (await res.json()) as T;
}

// login creates the user on first use. host is what the browser addressed; the API only serves
// logins for allowed hosts (localhost by default).
export async function login(name: string, host: string): Promise<{ name: string; token: string }> {
  const res = await fetch(`${API_URL}/login`, {
    method: "POST",
    headers: { "Content-Type": "application/json", "X-Forwarded-Host": host },
    body: JSON.stringify({ name }),
    cache: "no-store",
  });
  if (!res.ok) throw await errorFrom(res);
  return (await res.json()) as { name: string; token: string };
}

// `search` is the query string built by `apiSearch` in lib/query.ts.
export const listSessions = (search: string) => api<SessionList>(`/sessions?${search}`);
export const getStats = (search: string) => api<Stats>(`/stats?${search}`);
export const getSession = (id: string) => api<Session>(`/sessions/${encodeURIComponent(id)}`);

export const createTurn = (id: string, prompt: string) =>
  api<Turn>(`/sessions/${encodeURIComponent(id)}/turns`, {
    method: "POST",
    body: JSON.stringify({ prompt }),
  });

export const stopTurn = (id: string) =>
  api<Record<string, never>>(`/sessions/${encodeURIComponent(id)}/turns/current/stop`, { method: "POST" });

export const deleteSession = (id: string) =>
  api<Session>(`/sessions/${encodeURIComponent(id)}`, { method: "DELETE" });
