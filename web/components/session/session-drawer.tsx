"use client";

import { useEffect, useRef } from "react";
import type { Session } from "@/lib/types";
import { CloseButton } from "./close-button";
import { SessionPanel } from "./session-panel";

// Right-side sheet over the runs list. A modal <dialog> makes the page behind it inert, which
// keeps keyboard focus inside the drawer while the list keeps rendering and refreshing.
export function SessionDrawer({
  openId,
  session,
  error,
  onClose,
}: {
  openId: string | null;
  session: Session | null;
  error: string | null;
  onClose: () => void;
}) {
  if (!openId) return null;
  return (
    <Sheet onClose={onClose} returnFocusTo={openId}>
      {session ? (
        <SessionPanel key={session.id} session={session} onClose={onClose} />
      ) : (
        <div className="flex h-full flex-col">
          <div className="flex items-center justify-between border-b border-line px-5 py-3">
            <span className="font-mono text-xs text-muted">{openId}</span>
            <CloseButton onClose={onClose} />
          </div>
          <p className={`p-5 text-sm ${error ? "text-red-300" : "text-muted"}`}>{error ?? "Loading…"}</p>
        </div>
      )}
    </Sheet>
  );
}

function Sheet({ onClose, returnFocusTo, children }: { onClose: () => void; returnFocusTo: string; children: React.ReactNode }) {
  const ref = useRef<HTMLDialogElement>(null);
  const returnId = useRef(returnFocusTo);
  useEffect(() => {
    returnId.current = returnFocusTo;
  }, [returnFocusTo]);

  useEffect(() => {
    const dialog = ref.current;
    if (!dialog) return;
    const opener = document.activeElement instanceof HTMLElement ? document.activeElement : null;
    dialog.showModal();
    return () => {
      if (dialog.open) dialog.close();
      // Prefer the element that opened the drawer; a row click leaves focus on <body>, so fall
      // back to the visible link of the session's row.
      if (opener && opener !== document.body && opener.isConnected) {
        opener.focus();
        return;
      }
      const links = document.querySelectorAll<HTMLElement>(`[data-session-row="${CSS.escape(returnId.current)}"] a`);
      [...links].find((a) => a.offsetParent !== null)?.focus();
    };
  }, []);

  return (
    <dialog
      ref={ref}
      aria-label="Session details"
      onCancel={(e) => {
        e.preventDefault();
        onClose();
      }}
      onClick={(e) => {
        // The panel fills the dialog box, so a click that targets the dialog itself hit the backdrop.
        if (e.target === e.currentTarget) onClose();
      }}
      className="fixed inset-y-0 right-0 left-auto m-0 h-dvh max-h-none w-full max-w-none overflow-hidden border-l border-line bg-bg p-0 text-fg shadow-2xl shadow-black/60 backdrop:bg-black/50 md:w-[70vw]"
    >
      {children}
    </dialog>
  );
}
