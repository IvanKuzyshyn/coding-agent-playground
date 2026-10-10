import Link from "next/link";
import { notFound } from "next/navigation";
import { AutoRefresh } from "@/components/auto-refresh";
import { RelTime } from "@/components/rel-time";
import { StatusBadge } from "@/components/status-badge";
import { TokenBar } from "@/components/token-bar";
import { ApiError, getSession } from "@/lib/api";
import { TERMINAL_SESSION } from "@/lib/types";
import { SessionLive } from "./session-live";

export default async function SessionPage({ params }: PageProps<"/sessions/[id]">) {
  const { id } = await params;
  let session;
  try {
    session = await getSession(id);
  } catch (e) {
    if (e instanceof ApiError && e.status === 404) notFound();
    throw e;
  }
  const live = !TERMINAL_SESSION.includes(session.status);

  return (
    <div className="flex flex-col gap-3">
      <AutoRefresh ms={5000} />
      <header className="flex flex-col gap-2 rounded border border-line bg-panel p-3">
        <div className="flex flex-wrap items-center gap-2">
          <Link href="/sessions" className="text-xs text-muted hover:text-fg">
            ← sessions
          </Link>
          <span className="font-mono text-xs break-all text-fg">{session.id}</span>
          <StatusBadge status={session.status} />
        </div>
        <dl className="grid grid-cols-2 gap-x-4 gap-y-1 font-mono text-xs sm:grid-cols-4">
          <Field label="harness">{session.harness}</Field>
          <Field label="repo">{session.repo ?? "–"}</Field>
          <Field label="preset">{session.preset}</Field>
          <Field label="model">{session.model ?? "default"}</Field>
          <Field label="created">
            <RelTime iso={session.created_at} />
          </Field>
          <Field label="expires">{live ? <RelTime iso={session.expires_at} /> : "–"}</Field>
          <Field label="idle until">{live && session.idle_until ? <RelTime iso={session.idle_until} /> : "–"}</Field>
          <Field label="ended">{session.ended_at ? <RelTime iso={session.ended_at} /> : "–"}</Field>
        </dl>
        <div className="flex items-center gap-2">
          <span className="font-mono text-xs text-muted">tokens</span>
          <TokenBar used={session.tokens_used} budget={session.token_budget} wide />
        </div>
        {session.error && (
          <p className="rounded border border-red-500/40 bg-red-500/10 px-2 py-1 font-mono text-xs text-red-300">
            {session.error}
          </p>
        )}
      </header>
      <SessionLive session={session} />
    </div>
  );
}

function Field({ label, children }: { label: string; children: React.ReactNode }) {
  return (
    <div className="flex min-w-0 gap-1.5">
      <dt className="text-muted">{label}</dt>
      <dd className="truncate">{children}</dd>
    </div>
  );
}
