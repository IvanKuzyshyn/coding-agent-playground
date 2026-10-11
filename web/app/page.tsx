import { AutoRefresh } from "@/components/auto-refresh";
import { RunsView } from "@/components/runs/runs-view";
import { StatTiles } from "@/components/stat-tiles";
import { TopNav } from "@/components/top-nav";
import { ApiError, getSession, getStats, listSessions } from "@/lib/api";
import { requireAuth } from "@/lib/auth";
import { apiSearch, parseQuery, statsSearch } from "@/lib/query";
import type { Session } from "@/lib/types";

async function loadOpenSession(id: string | null): Promise<{ session: Session | null; error: string | null }> {
  if (!id) return { session: null, error: null };
  try {
    return { session: await getSession(id), error: null };
  } catch (e) {
    if (e instanceof ApiError) return { session: null, error: e.status === 404 ? "Session not found." : e.message };
    throw e;
  }
}

export default async function Home({ searchParams }: PageProps<"/">) {
  const { name } = await requireAuth();
  const query = parseQuery(await searchParams);
  const [list, stats, open] = await Promise.all([
    listSessions(apiSearch(query, name)),
    getStats(statsSearch(query, name)),
    loadOpenSession(query.session),
  ]);

  return (
    <div className="min-h-screen">
      <TopNav user={name} />
      <main className="mx-auto flex max-w-[1600px] flex-col gap-4 px-3 py-5 sm:px-6">
        <div>
          <h1 className="text-xl font-semibold text-fg">Runs</h1>
          <p className="text-sm text-muted">
            Agent sessions of all users, running in sandboxes. Totals follow the filters. Updates every few seconds.
          </p>
        </div>
        <AutoRefresh ms={3000} />
        <StatTiles stats={stats} filtered={query.q !== "" || query.status.length > 0 || query.harness.length > 0 || query.mine} />
        <RunsView user={name} list={list} openSession={open.session} openError={open.error} />
      </main>
    </div>
  );
}
