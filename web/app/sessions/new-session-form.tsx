"use client";

import { useActionState } from "react";
import { createSession } from "@/app/actions";

const input =
  "rounded border border-line bg-bg px-2 py-1.5 font-mono text-sm outline-none focus:border-accent";

export function NewSessionForm({ presets }: { presets: string[] }) {
  const [state, action, pending] = useActionState(createSession, {});
  return (
    <form action={action} className="flex flex-col gap-3">
      <div className="grid grid-cols-1 gap-3 sm:grid-cols-3">
        <label className="flex flex-col gap-1 text-xs text-muted">
          harness
          <select name="harness" className={input} defaultValue="claude-code">
            <option value="claude-code">claude-code</option>
            <option value="codex">codex</option>
          </select>
        </label>
        <label className="flex flex-col gap-1 text-xs text-muted">
          repo (optional)
          <input
            name="repo"
            placeholder="owner/name"
            pattern="[A-Za-z0-9_.\-]+/[A-Za-z0-9_.\-]+"
            autoCapitalize="off"
            autoCorrect="off"
            className={input}
          />
        </label>
        <label className="flex flex-col gap-1 text-xs text-muted">
          preset
          <select name="preset" className={input} defaultValue={presets.includes("default") ? "default" : presets[0]}>
            {presets.map((p) => (
              <option key={p} value={p}>
                {p}
              </option>
            ))}
          </select>
        </label>
      </div>
      <label className="flex flex-col gap-1 text-xs text-muted">
        prompt
        <textarea name="prompt" required rows={4} className={`${input} resize-y`} />
      </label>
      <div className="flex items-center gap-3">
        <button
          disabled={pending}
          className="rounded bg-accent px-3 py-1.5 text-sm font-medium text-black disabled:opacity-50"
        >
          {pending ? "Creating…" : "Create session"}
        </button>
        {state.error && <p className="text-sm text-red-400">{state.error}</p>}
      </div>
    </form>
  );
}
