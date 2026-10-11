import { Examples } from "@/components/examples";
import { TopNav } from "@/components/top-nav";
import { requireAuth } from "@/lib/auth";
import { PUBLIC_API_URL } from "@/lib/public-api";

export default async function ExamplesPage() {
  const { name } = await requireAuth();
  return (
    <div className="min-h-screen">
      <TopNav user={name} active="examples" />
      <main className="mx-auto flex max-w-3xl flex-col gap-5 px-3 py-5 sm:px-6">
        <div className="flex flex-col gap-1">
          <h1 className="text-xl font-semibold text-fg">Examples</h1>
          <p className="text-sm text-muted">
            Ready-to-run scenarios for trying Terrarium from a terminal. Pick the harness you have configured; each
            command starts a session, waits for the agent and prints the result. They need <code>curl</code> and{" "}
            <code>jq</code> (both come with <code>mise install</code>) and work in bash and zsh. Copy puts your real API
            token on the clipboard. Sessions also appear under Runs while they work.
          </p>
        </div>
        <Examples api={PUBLIC_API_URL} />
      </main>
    </div>
  );
}
