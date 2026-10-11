"use client";

import { useSyncExternalStore } from "react";
import { CopyBlock } from "@/components/copy-block";
import { HARNESS_LABELS, HarnessIcon } from "@/components/harness-icon";
import { EXAMPLES, EXAMPLE_MODELS, exampleScript } from "@/lib/examples";
import type { Harness } from "@/lib/types";

const HARNESSES: Harness[] = ["claude-code", "codex"];
const HARNESS_KEY = "terrarium.examples.harness";
const COLLAPSED_KEY = "terrarium.examples.collapsed";
const CHANGED = "terrarium-examples-changed";

function subscribe(onChange: () => void) {
  window.addEventListener("storage", onChange);
  window.addEventListener(CHANGED, onChange);
  return () => {
    window.removeEventListener("storage", onChange);
    window.removeEventListener(CHANGED, onChange);
  };
}

function save(key: string, value: string) {
  localStorage.setItem(key, value);
  window.dispatchEvent(new Event(CHANGED));
}

function savedHarness(): Harness {
  return localStorage.getItem(HARNESS_KEY) === "codex" ? "codex" : "claude-code";
}

// A string snapshot: useSyncExternalStore compares snapshots by identity, so a fresh Set would loop.
function savedCollapsed(): string {
  return localStorage.getItem(COLLAPSED_KEY) ?? "";
}

// One harness choice drives every example: most people have only one subscription configured, so
// they pick once and it is remembered. Collapsed examples are remembered too, to mark the ones
// already tried.
export function Examples({ api }: { api: string }) {
  const harness = useSyncExternalStore(subscribe, savedHarness, (): Harness => "claude-code");
  const choose = (h: Harness) => save(HARNESS_KEY, h);
  const collapsed = new Set(useSyncExternalStore(subscribe, savedCollapsed, () => "").split(",").filter(Boolean));
  const setCollapsed = (ids: Iterable<string>) => save(COLLAPSED_KEY, [...ids].join(","));
  const toggle = (id: string, open: boolean) => {
    if (open !== collapsed.has(id)) return;
    const next = new Set(collapsed);
    if (open) next.delete(id);
    else next.add(id);
    setCollapsed(next);
  };

  return (
    <div className="flex flex-col gap-4">
      <div className="flex justify-end gap-3 text-xs">
        <button type="button" onClick={() => setCollapsed(EXAMPLES.map((ex) => ex.id))} className="text-muted hover:text-fg">
          Collapse all
        </button>
        <button type="button" onClick={() => setCollapsed([])} className="text-muted hover:text-fg">
          Expand all
        </button>
      </div>
      {EXAMPLES.map((ex, i) => (
        <section key={ex.id} id={ex.id} className="relative rounded-md border border-line bg-panel">
          <details open={!collapsed.has(ex.id)} onToggle={(e) => toggle(ex.id, e.currentTarget.open)} className="group">
            <summary className="flex items-center gap-2 p-3 text-sm font-semibold text-fg sm:px-4">
              <span className="flex h-5 w-5 items-center justify-center rounded-full bg-panel2 font-mono text-[11px] text-muted">
                {i + 1}
              </span>
              <h2>{ex.title}</h2>
            </summary>
            <div className="flex flex-col gap-2 px-3 pb-3 sm:px-4 sm:pb-4">
              <p className="text-sm text-muted">{ex.summary}</p>
              <CopyBlock label={`${ex.title} command`} text={exampleScript(ex, harness, api)} withToken />
              <p className="text-xs text-muted">
                <span className="text-fg">Expect:</span> {ex.expect}{" "}
                <span className="font-mono">model {EXAMPLE_MODELS[harness]}</span>
              </p>
            </div>
          </details>
          {/* Outside <summary>: a click there would also toggle the example. */}
          {!collapsed.has(ex.id) && (
            <div
              role="tablist"
              aria-label={`Harness for ${ex.title}`}
              className="absolute top-2.5 right-3 flex rounded-md border border-line bg-panel2 p-0.5 sm:right-4"
            >
              {HARNESSES.map((h) => (
                <button
                  key={h}
                  type="button"
                  role="tab"
                  aria-selected={h === harness}
                  onClick={() => choose(h)}
                  className={`flex items-center gap-1.5 rounded px-2 py-0.5 text-xs ${h === harness ? "bg-panel text-fg" : "text-muted hover:text-fg"}`}
                >
                  <HarnessIcon harness={h} size={16} />
                  {HARNESS_LABELS[h]}
                </button>
              ))}
            </div>
          )}
        </section>
      ))}
    </div>
  );
}
