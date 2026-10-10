"use client";

import { useState } from "react";

export function CopyBlock({ text, label }: { text: string; label: string }) {
  const [copied, setCopied] = useState(false);
  return (
    <div className="relative rounded-md border border-line bg-panel2">
      <pre className="px-3 py-2.5 pr-16 font-mono text-xs leading-relaxed break-words whitespace-pre-wrap text-fg">{text}</pre>
      <button
        type="button"
        aria-label={`Copy ${label}`}
        onClick={async () => {
          await navigator.clipboard.writeText(text);
          setCopied(true);
          setTimeout(() => setCopied(false), 1500);
        }}
        className="absolute top-1.5 right-1.5 rounded border border-line bg-panel px-2 py-0.5 text-[11px] text-muted hover:text-fg"
      >
        {copied ? "Copied" : "Copy"}
      </button>
    </div>
  );
}
