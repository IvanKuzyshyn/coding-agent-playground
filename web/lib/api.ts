import "server-only";

import { redirect } from "next/navigation";
import { requireAuth } from "./auth";
import type { Preset, Session, Turn } from "./types";

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

export async function whoami(token: string): Promise<{ name: string }> {
  const res = await apiRaw(token, "/whoami");
  if (!res.ok) throw await errorFrom(res);
  return (await res.json()) as { name: string };
}

export const listSessions = () => api<{ sessions: Session[] }>("/sessions").then((r) => r.sessions);
export const getSession = (id: string) => api<Session>(`/sessions/${encodeURIComponent(id)}`);
export const listPresets = () => api<{ presets: Preset[] }>("/presets").then((r) => r.presets);

export const createSession = (body: {
  harness: string;
  prompt: string;
  repo?: string;
  preset?: string;
}) => api<Session>("/sessions", { method: "POST", body: JSON.stringify(body) });

export const createTurn = (id: string, prompt: string) =>
  api<Turn>(`/sessions/${encodeURIComponent(id)}/turns`, {
    method: "POST",
    body: JSON.stringify({ prompt }),
  });

export const stopTurn = (id: string) =>
  api<Record<string, never>>(`/sessions/${encodeURIComponent(id)}/turns/current/stop`, { method: "POST" });

export const deleteSession = (id: string) =>
  api<Session>(`/sessions/${encodeURIComponent(id)}`, { method: "DELETE" });
