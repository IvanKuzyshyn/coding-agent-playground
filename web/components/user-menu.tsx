"use client";

import { useEffect, useRef, useState } from "react";
import { logout } from "@/app/actions";
import { copyWithToken, TOKEN_MASK } from "@/lib/token";

export function UserMenu({ user }: { user: string }) {
  const [open, setOpen] = useState(false);
  const [copied, setCopied] = useState(false);
  const ref = useRef<HTMLDivElement>(null);

  useEffect(() => {
    if (!open) return;
    const close = (e: MouseEvent | KeyboardEvent) => {
      if (e instanceof KeyboardEvent ? e.key === "Escape" : !ref.current?.contains(e.target as Node)) setOpen(false);
    };
    document.addEventListener("mousedown", close);
    document.addEventListener("keydown", close);
    return () => {
      document.removeEventListener("mousedown", close);
      document.removeEventListener("keydown", close);
    };
  }, [open]);

  async function copyToken() {
    await copyWithToken(TOKEN_MASK);
    setCopied(true);
    setTimeout(() => {
      setCopied(false);
      setOpen(false);
    }, 1000);
  }

  return (
    <div ref={ref} className="relative ml-auto">
      <button
        type="button"
        aria-haspopup="menu"
        aria-expanded={open}
        onClick={() => setOpen((o) => !o)}
        className="flex items-center gap-2 rounded-md border border-line px-2 py-1 hover:border-muted"
      >
        <span
          aria-hidden
          className="flex h-5 w-5 items-center justify-center rounded-full bg-panel2 font-mono text-[10px] uppercase text-muted"
        >
          {user.slice(0, 2)}
        </span>
        <span className="font-mono text-xs text-fg">{user}</span>
        <svg viewBox="0 0 12 12" width="10" height="10" aria-hidden className="text-muted">
          <path d="M3 4.5 6 7.5 9 4.5" fill="none" stroke="currentColor" strokeWidth="1.3" strokeLinecap="round" />
        </svg>
      </button>
      {open && (
        <div role="menu" className="absolute right-0 mt-1 w-40 overflow-hidden rounded-md border border-line bg-panel shadow-lg">
          <button type="button" role="menuitem" onClick={copyToken} className="block w-full px-3 py-2 text-left text-sm text-fg hover:bg-panel2">
            {copied ? "Token copied" : "Copy token"}
          </button>
          <form action={logout}>
            <button role="menuitem" className="block w-full border-t border-line px-3 py-2 text-left text-sm text-fg hover:bg-panel2">
              Log out
            </button>
          </form>
        </div>
      )}
    </div>
  );
}
