import type { Harness } from "@/lib/types";

export const HARNESS_LABELS: Record<Harness, string> = {
  "claude-code": "Claude Code",
  codex: "Codex",
};

// Original glyphs (not vendor logos): a prompt chevron with a spark for Claude Code, braces in a
// hexagon for Codex.
export function HarnessIcon({ harness, size = 28 }: { harness: Harness; size?: number }) {
  const label = HARNESS_LABELS[harness] ?? harness;
  return (
    <span
      role="img"
      aria-label={label}
      title={label}
      className="inline-flex shrink-0 items-center justify-center rounded-md border border-line bg-panel2"
      style={{ width: size, height: size }}
    >
      {harness === "codex" ? (
        <svg viewBox="0 0 16 16" width={size * 0.6} height={size * 0.6} fill="none" className="text-teal-300" aria-hidden>
          <path d="M8 1.5 13.6 4.75v6.5L8 14.5l-5.6-3.25v-6.5Z" stroke="currentColor" strokeWidth="1.2" strokeLinejoin="round" />
          <path
            d="M6.6 5.6c-.9 0-.9.6-.9 1.2s-.2 1.2-.8 1.2c.6 0 .8.6.8 1.2s0 1.2.9 1.2M9.4 5.6c.9 0 .9.6.9 1.2s.2 1.2.8 1.2c-.6 0-.8.6-.8 1.2s0 1.2-.9 1.2"
            stroke="currentColor"
            strokeWidth="1.1"
            strokeLinecap="round"
          />
        </svg>
      ) : (
        <svg viewBox="0 0 16 16" width={size * 0.6} height={size * 0.6} fill="none" className="text-amber-300" aria-hidden>
          <path d="M2.5 4.5 6 8l-3.5 3.5" stroke="currentColor" strokeWidth="1.5" strokeLinecap="round" strokeLinejoin="round" />
          <path d="M7.5 12h3" stroke="currentColor" strokeWidth="1.5" strokeLinecap="round" />
          <path d="M12.5 2v4M10.5 4h4" stroke="currentColor" strokeWidth="1.2" strokeLinecap="round" />
        </svg>
      )}
    </span>
  );
}
