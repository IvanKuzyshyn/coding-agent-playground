"use server";

import { redirect } from "next/navigation";
import { revalidatePath } from "next/cache";
import * as api from "@/lib/api";
import { clearAuth, setAuth } from "@/lib/auth";

export interface ActionState {
  error?: string;
  ok?: string;
}

// Only API errors become inline messages; redirects and unexpected errors must propagate.
async function attempt(fn: () => Promise<string | void>): Promise<ActionState> {
  try {
    const ok = await fn();
    return ok ? { ok } : {};
  } catch (e) {
    if (e instanceof api.ApiError) return { error: e.message };
    throw e;
  }
}

function field(form: FormData, name: string): string {
  const v = form.get(name);
  return typeof v === "string" ? v.trim() : "";
}

export async function login(_: ActionState, form: FormData): Promise<ActionState> {
  const token = field(form, "token");
  if (!token) return { error: "Paste your API token." };
  let name: string;
  try {
    name = (await api.whoami(token)).name;
  } catch (e) {
    if (e instanceof api.ApiError) {
      return { error: e.status === 401 ? "Token rejected by the API." : e.message };
    }
    return { error: `API unreachable at ${api.API_URL}.` };
  }
  await setAuth({ token, name });
  redirect("/sessions");
}

export async function logout(): Promise<void> {
  await clearAuth();
  redirect("/login");
}

export async function createSession(_: ActionState, form: FormData): Promise<ActionState> {
  const prompt = field(form, "prompt");
  if (!prompt) return { error: "Prompt is required." };
  const repo = field(form, "repo");
  const preset = field(form, "preset");
  let id = "";
  const state = await attempt(async () => {
    const s = await api.createSession({
      harness: field(form, "harness"),
      prompt,
      ...(repo ? { repo } : {}),
      ...(preset ? { preset } : {}),
    });
    id = s.id;
  });
  if (state.error) return state;
  redirect(`/sessions/${id}`);
}

export async function followUp(_: ActionState, form: FormData): Promise<ActionState> {
  const id = field(form, "id");
  const prompt = field(form, "prompt");
  if (!prompt) return { error: "Prompt is required." };
  const state = await attempt(async () => {
    const t = await api.createTurn(id, prompt);
    return `Turn ${t.seq} ${t.status}.`;
  });
  revalidatePath(`/sessions/${id}`);
  return state;
}

export async function stopTurn(_: ActionState, form: FormData): Promise<ActionState> {
  const id = field(form, "id");
  const state = await attempt(async () => {
    await api.stopTurn(id);
    return "Stop requested.";
  });
  revalidatePath(`/sessions/${id}`);
  return state;
}

export async function deleteSession(_: ActionState, form: FormData): Promise<ActionState> {
  const id = field(form, "id");
  const state = await attempt(async () => {
    await api.deleteSession(id);
    return "Session stopping.";
  });
  revalidatePath(`/sessions/${id}`);
  return state;
}
