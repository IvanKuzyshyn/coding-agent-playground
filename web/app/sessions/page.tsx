import Link from "next/link";
import { AutoRefresh } from "@/components/auto-refresh";
import { RelTime } from "@/components/rel-time";
import { StatusBadge } from "@/components/status-badge";
import { TokenBar } from "@/components/token-bar";
import { listPresets, listSessions } from "@/lib/api";
import { shortId } from "@/lib/format";
import { NewSessionForm } from "./new-session-form";

export default async function SessionsPage() {
  const [sessions, presets] = await Promise.all([listSessions(), listPresets()]);
  return (
    <div className="flex flex-col gap-4">
      <AutoRefresh ms={3000} />
      <details className="rounded border border-line bg-panel" open={sessions.length === 0}>
        <summary className="px-3 py-2 text-sm font-medium">New session</summary>
        <div className="border-t border-line p-3">
          <NewSessionForm presets={presets.map((p) => p.name)} />
        </div>
      </details>

      <section>
        <h2 className="mb-2 text-xs font-medium tracking-wide text-muted uppercase">
          Sessions <span className="font-mono">({sessions.length})</span>
        </h2>
        {sessions.length === 0 ? (
          <p className="text-sm text-muted">No sessions yet.</p>
        ) : (
          <>
            <table className="hidden w-full border-collapse text-sm md:table">
              <thead>
                <tr className="border-b border-line text-left text-xs text-muted">
                  <th className="py-1.5 pr-3 font-normal">id</th>
                  <th className="py-1.5 pr-3 font-normal">status</th>
                  <th className="py-1.5 pr-3 font-normal">harness</th>
                  <th className="py-1.5 pr-3 font-normal">repo</th>
                  <th className="py-1.5 pr-3 font-normal">turns</th>
                  <th className="py-1.5 pr-3 font-normal">tokens</th>
                  <th className="py-1.5 pr-3 font-normal">age</th>
                  <th className="py-1.5 font-normal">error</th>
                </tr>
              </thead>
              <tbody>
                {sessions.map((s) => (
                  <tr key={s.id} className="border-b border-line/60 hover:bg-panel">
                    <td className="py-1.5 pr-3 font-mono">
                      <Link href={`/sessions/${s.id}`} className="text-accent hover:underline">
                        {shortId(s.id)}
                      </Link>
                    </td>
                    <td className="py-1.5 pr-3">
                      <StatusBadge status={s.status} />
                    </td>
                    <td className="py-1.5 pr-3 font-mono text-xs">{s.harness}</td>
                    <td className="max-w-48 truncate py-1.5 pr-3 font-mono text-xs">{s.repo ?? "–"}</td>
                    <td className="py-1.5 pr-3 font-mono text-xs">{s.turn_count}</td>
                    <td className="py-1.5 pr-3">
                      <TokenBar used={s.tokens_used} budget={s.token_budget} />
                    </td>
                    <td className="py-1.5 pr-3 font-mono text-xs text-muted">
                      <RelTime iso={s.created_at} />
                    </td>
                    <td className="max-w-64 truncate py-1.5 text-xs text-red-400" title={s.error}>
                      {s.error ?? ""}
                    </td>
                  </tr>
                ))}
              </tbody>
            </table>

            <ul className="flex flex-col gap-2 md:hidden">
              {sessions.map((s) => (
                <li key={s.id}>
                  <Link
                    href={`/sessions/${s.id}`}
                    className="flex flex-col gap-1.5 rounded border border-line bg-panel p-2.5"
                  >
                    <div className="flex items-center justify-between gap-2">
                      <span className="font-mono text-accent">{shortId(s.id)}</span>
                      <StatusBadge status={s.status} />
                    </div>
                    <div className="flex flex-wrap gap-x-3 font-mono text-xs text-muted">
                      <span>{s.harness}</span>
                      <span>{s.repo ?? "no repo"}</span>
                      <span>{s.turn_count} turns</span>
                      <RelTime iso={s.created_at} />
                    </div>
                    <TokenBar used={s.tokens_used} budget={s.token_budget} wide />
                    {s.error && <p className="text-xs text-red-400">{s.error}</p>}
                  </Link>
                </li>
              ))}
            </ul>
          </>
        )}
      </section>
    </div>
  );
}
