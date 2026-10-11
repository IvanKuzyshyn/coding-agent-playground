"use client";

import { useEffect, useState } from "react";
import { relTime } from "@/lib/format";

export function RelTime({ iso }: { iso: string }) {
  const [now, setNow] = useState(() => Date.now());
  useEffect(() => {
    const t = setInterval(() => setNow(Date.now()), 5000);
    return () => clearInterval(t);
  }, []);
  return (
    <time dateTime={iso} title={iso} suppressHydrationWarning>
      {relTime(iso, now)}
    </time>
  );
}
