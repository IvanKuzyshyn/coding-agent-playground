"use client";

import { useRouter } from "next/navigation";
import { useEffect } from "react";

// Re-renders the server components on an interval while the tab is visible.
export function AutoRefresh({ ms }: { ms: number }) {
  const router = useRouter();
  useEffect(() => {
    const t = setInterval(() => {
      if (document.visibilityState === "visible") router.refresh();
    }, ms);
    return () => clearInterval(t);
  }, [router, ms]);
  return null;
}
