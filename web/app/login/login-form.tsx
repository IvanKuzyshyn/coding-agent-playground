"use client";

import { useActionState } from "react";
import { login } from "@/app/actions";

export function LoginForm() {
  const [state, action, pending] = useActionState(login, {});
  return (
    <form action={action} className="flex flex-col gap-3">
      <input
        name="name"
        autoComplete="username"
        autoCapitalize="none"
        spellCheck={false}
        autoFocus
        required
        maxLength={39}
        placeholder="e.g. ivan.kuzyshyn"
        aria-describedby="name-hint"
        className="rounded border border-line bg-panel px-3 py-2 font-mono text-sm outline-none focus:border-accent"
      />
      <p id="name-hint" className="-mt-1 text-xs text-muted">
        Up to 39 characters: lowercase letters, digits and <code className="font-mono">.</code>{" "}
        <code className="font-mono">_</code> <code className="font-mono">-</code>, no spaces. Example:{" "}
        <code className="font-mono text-fg">ivan.kuzyshyn</code>
      </p>
      <button
        disabled={pending}
        className="rounded bg-accent px-3 py-2 text-sm font-medium text-black disabled:opacity-50"
      >
        {pending ? "Logging in…" : "Log in"}
      </button>
      {state.error && <p className="text-sm text-red-400">{state.error}</p>}
    </form>
  );
}
