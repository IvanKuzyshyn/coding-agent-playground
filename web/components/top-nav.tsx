import Link from "next/link";
import { logout } from "@/app/actions";

export function TopNav({ user }: { user: string }) {
  return (
    <header className="sticky top-0 z-20 border-b border-line bg-bg/95 backdrop-blur">
      <div className="flex h-12 items-center gap-4 px-3 sm:px-6">
        <Link href="/" className="flex items-center gap-2 text-sm font-semibold text-fg">
          <svg viewBox="0 0 20 20" width="20" height="20" fill="none" aria-hidden className="text-emerald-400">
            <rect x="2.5" y="5.5" width="15" height="12" rx="2" stroke="currentColor" strokeWidth="1.3" />
            <path d="M10 15V9.5M10 11c0-2 1.6-3.2 3.5-3.2 0 2-1.6 3.2-3.5 3.2ZM10 12.2c0-1.6-1.2-2.6-2.8-2.6 0 1.6 1.2 2.6 2.8 2.6Z" stroke="currentColor" strokeWidth="1.2" strokeLinejoin="round" />
            <path d="M5 2.5h10" stroke="currentColor" strokeWidth="1.3" strokeLinecap="round" />
          </svg>
          Terrarium
        </Link>
        <nav aria-label="Main">
          <Link href="/" aria-current="page" className="rounded-md bg-panel2 px-2.5 py-1 text-sm text-fg">
            Runs
          </Link>
        </nav>
        <form action={logout} className="ml-auto flex items-center gap-3">
          <span className="flex items-center gap-2 rounded-md border border-line px-2 py-1">
            <span
              aria-hidden
              className="flex h-5 w-5 items-center justify-center rounded-full bg-panel2 font-mono text-[10px] uppercase text-muted"
            >
              {user.slice(0, 2)}
            </span>
            <span className="font-mono text-xs text-fg">{user}</span>
          </span>
          <button className="text-xs text-muted hover:text-fg">Log out</button>
        </form>
      </div>
    </header>
  );
}
