"use client";

import { useSyncExternalStore } from "react";
import { CopyBlock } from "@/components/copy-block";
import { HARNESS_LABELS, HarnessIcon } from "@/components/harness-icon";
import { EXAMPLES, EXAMPLE_MODELS, exampleScript } from "@/lib/examples";
import type { Harness } from "@/lib/types";

const HARNESSES: Harness[] = ["claude-code", "codex"];
const STORAGE_KEY = "terrarium.examples.harness";
const CHANGED = "terrarium-examples-harness";

function subscribe(onChange: () => void) {
  window.addEventListener("storage", onChange);
  window.addEventListener(CHANGED, onChange);
  return () => {
    window.removeEventListener("storage", onChange);
    window.removeEventListener(CHANGED, onChange);
  };
}

function savedHarness(): Harness {
  return localStorage.getItem(STORAGE_KEY) === "codex" ? "codex" : "claude-code";
}

// One harness choice drives every example: most people have only one subscription configured, so
// they pick once and it is remembered.
export function Examples({ api }: { api: string }) {
  const harness = useSyncExternalStore(subscribe, savedHarness, (): Harness => "claude-code");
  const choose = (h: Harness) => {
    localStorage.setItem(STORAGE_KEY, h);
    window.dispatchEvent(new Event(CHANGED));
  };

  return (
    <div className="flex flex-col gap-4">
      {EXAMPLES.map((ex, i) => (
        <section key={ex.id} id={ex.id} className="flex flex-col gap-2 rounded-md border border-line bg-panel p-3 sm:p-4">
          <div className="flex flex-wrap items-start justify-between gap-2">
            <h2 className="flex items-center gap-2 text-sm font-semibold text-fg">
              <span className="flex h-5 w-5 items-center justify-center rounded-full bg-panel2 font-mono text-[11px] text-muted">
                {i + 1}
              </span>
              {ex.title}
            </h2>
            <div role="tablist" aria-label={`Harness for ${ex.title}`} className="flex rounded-md border border-line bg-panel2 p-0.5">
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
          </div>
          <p className="text-sm text-muted">{ex.summary}</p>
          <CopyBlock label={`${ex.title} command`} text={exampleScript(ex, harness, api)} withToken />
          <p className="text-xs text-muted">
            <span className="text-fg">Expect:</span> {ex.expect}{" "}
            <span className="font-mono">model {EXAMPLE_MODELS[harness]}</span>
          </p>
        </section>
      ))}
    </div>
  );
}
