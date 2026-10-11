"use server";

import { headers } from "next/headers";
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
  const requested = field(form, "name");
  if (!requested) return { error: "Enter a user name." };
  let user: { name: string; token: string };
  try {
    user = await api.login(requested, (await headers()).get("host") ?? "");
  } catch (e) {
    if (e instanceof api.ApiError) return { error: e.message };
    return { error: `API unreachable at ${api.API_URL}.` };
  }
  await setAuth(user);
  redirect("/");
}

export async function logout(): Promise<void> {
  await clearAuth();
  redirect("/login");
}

export async function followUp(_: ActionState, form: FormData): Promise<ActionState> {
  const id = field(form, "id");
  const prompt = field(form, "prompt");
  if (!prompt) return { error: "Prompt is required." };
  const state = await attempt(async () => {
    const t = await api.createTurn(id, prompt);
    return `Turn ${t.seq} ${t.status}.`;
  });
  revalidatePath("/");
  return state;
}

export async function stopTurn(_: ActionState, form: FormData): Promise<ActionState> {
  const id = field(form, "id");
  const state = await attempt(async () => {
    await api.stopTurn(id);
    return "Stop requested.";
  });
  revalidatePath("/");
  return state;
}

export async function deleteSession(_: ActionState, form: FormData): Promise<ActionState> {
  const id = field(form, "id");
  const state = await attempt(async () => {
    await api.deleteSession(id);
    return "Session stopping.";
  });
  revalidatePath("/");
  return state;
}
