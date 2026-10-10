import Link from "next/link";
import { Logo } from "@/components/logo";
import { UserMenu } from "@/components/user-menu";

const LINKS = [
  { href: "/", label: "Runs", key: "runs" },
  { href: "/mcp", label: "MCP", key: "mcp" },
] as const;

export function TopNav({ user, active = "runs" }: { user: string; active?: (typeof LINKS)[number]["key"] }) {
  return (
    <header className="sticky top-0 z-20 border-b border-line bg-bg/95 backdrop-blur">
      <div className="flex h-12 items-center gap-4 px-3 sm:px-6">
        <Link href="/" className="flex items-center gap-2 text-sm font-semibold text-fg">
          <Logo size={22} />
          Terrarium
        </Link>
        <nav aria-label="Main" className="flex items-center gap-1">
          {LINKS.map((l) => (
            <Link
              key={l.key}
              href={l.href}
              aria-current={l.key === active ? "page" : undefined}
              className={`rounded-md px-2.5 py-1 text-sm ${l.key === active ? "bg-panel2 text-fg" : "text-muted hover:text-fg"}`}
            >
              {l.label}
            </Link>
          ))}
        </nav>
        <UserMenu user={user} />
      </div>
    </header>
  );
}
