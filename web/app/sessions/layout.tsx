import Link from "next/link";
import { logout } from "@/app/actions";
import { requireAuth } from "@/lib/auth";

export default async function SessionsLayout({ children }: LayoutProps<"/sessions">) {
  const { name } = await requireAuth();
  return (
    <div className="min-h-screen">
      <header className="sticky top-0 z-20 flex h-10 items-center justify-between border-b border-line bg-bg/95 px-3 backdrop-blur sm:px-4">
        <Link href="/sessions" className="font-mono text-sm text-fg">
          terrarium
        </Link>
        <form action={logout} className="flex items-center gap-3">
          <span className="font-mono text-xs text-muted">{name}</span>
          <button className="text-xs text-muted hover:text-fg">log out</button>
        </form>
      </header>
      <main className="mx-auto max-w-6xl px-3 py-4 sm:px-4">{children}</main>
    </div>
  );
}
