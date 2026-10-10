"use client";

export function CloseButton({ onClose }: { onClose: () => void }) {
  return (
    <button
      type="button"
      onClick={onClose}
      aria-label="Close"
      autoFocus
      className="flex h-8 w-8 shrink-0 items-center justify-center rounded-md border border-line text-muted hover:bg-panel2 hover:text-fg"
    >
      <svg viewBox="0 0 16 16" width="14" height="14" fill="none" stroke="currentColor" strokeWidth="1.5" strokeLinecap="round" aria-hidden>
        <path d="m4 4 8 8M12 4l-8 8" />
      </svg>
    </button>
  );
}
