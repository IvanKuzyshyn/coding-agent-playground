"use client";

import { useActionState } from "react";
import { login } from "@/app/actions";

export function LoginForm() {
  const [state, action, pending] = useActionState(login, {});
  return (
    <form action={action} className="flex flex-col gap-3">
      <input
        name="token"
        type="password"
        autoComplete="off"
        autoFocus
        required
        placeholder="API token"
        className="rounded border border-line bg-panel px-3 py-2 font-mono text-sm outline-none focus:border-accent"
      />
      <button
        disabled={pending}
        className="rounded bg-accent px-3 py-2 text-sm font-medium text-black disabled:opacity-50"
      >
        {pending ? "Checking…" : "Log in"}
      </button>
      {state.error && <p className="text-sm text-red-400">{state.error}</p>}
    </form>
  );
}
