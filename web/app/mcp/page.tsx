import type { ReactNode } from "react";
import { CopyBlock } from "@/components/copy-block";
import { TopNav } from "@/components/top-nav";
import { requireAuth } from "@/lib/auth";
import { PUBLIC_API_URL } from "@/lib/public-api";
import { TOKEN_MASK } from "@/lib/token";


const TOOLS: [string, string][] = [
  ["list_options", "harnesses and presets (limits, allowed hosts, lifecycle)"],
  ["create_session", "start an agent in a fresh sandbox"],
  ["wait_for_session", "block until it finishes a turn or ends"],
  ["get_session", "status, cost, turns and results"],
  ["list_sessions", "all sessions, filtered by owner, status, harness or name"],
  ["send_follow_up", "queue another prompt (sessions created with keep_idle)"],
  ["terminate_session", "end it and destroy the sandbox"],
];

export default async function McpPage() {
  const { name } = await requireAuth();
  const url = `${PUBLIC_API_URL}/mcp`;
  const claude = `claude mcp add --transport http --scope user terrarium ${url} \\\n  --header "Authorization: Bearer ${TOKEN_MASK}"`;
  const codex = `export TERRARIUM_TOKEN=${TOKEN_MASK}\ncodex mcp add terrarium --url ${url} \\\n  --bearer-token-env-var TERRARIUM_TOKEN`;
  const pi = `export TERRARIUM_TOKEN=${TOKEN_MASK}\npi mcp add terrarium --url ${url} \\\n  --bearer-token-env-var TERRARIUM_TOKEN`;
  const json = JSON.stringify(
    { mcpServers: { terrarium: { type: "http", url, headers: { Authorization: `Bearer ${TOKEN_MASK}` } } } },
    null,
    2,
  );

  return (
    <div className="min-h-screen">
      <TopNav user={name} active="mcp" />
      <main className="mx-auto flex max-w-3xl flex-col gap-6 px-3 py-5 sm:px-6">
        <div>
          <h1 className="text-xl font-semibold text-fg">Connect an agent over MCP</h1>
          <p className="text-sm text-muted">
            Let another coding agent start, follow and end Terrarium sessions. Sessions it creates are yours and appear
            under Runs.
          </p>
        </div>

        <Step n={1} title="Endpoint">
          <CopyBlock label="endpoint" text={url} />
        </Step>

        <Step n={2} title="Add it to your agent">
          <p className="text-sm text-muted">
            The commands include your API token, hidden here; Copy puts the real token on the clipboard.
          </p>
          <h3 className="pt-1 text-xs font-medium tracking-widest text-muted uppercase">Claude Code</h3>
          <CopyBlock label="Claude Code command" text={claude} withToken />
          <p className="text-xs text-muted">Claude Code stores the token in its config.</p>
          <h3 className="pt-2 text-xs font-medium tracking-widest text-muted uppercase">Codex</h3>
          <CopyBlock label="Codex command" text={codex} withToken />
          <p className="text-xs text-muted">
            Codex reads the token from TERRARIUM_TOKEN when it starts; keep the export in your shell profile.
          </p>
          <h3 className="pt-2 text-xs font-medium tracking-widest text-muted uppercase">Pi</h3>
          <CopyBlock label="Pi command" text={pi} withToken />
          <p className="text-xs text-muted">
            Pi reads the token from TERRARIUM_TOKEN when it starts; keep the export in your shell profile.
            Configuration is saved to ~/.pi/agent/mcp.json. Run /reload in an existing Pi session after adding it.
          </p>
          <h3 className="pt-2 text-xs font-medium tracking-widest text-muted uppercase">Other clients (JSON)</h3>
          <CopyBlock label="JSON config" text={json} withToken />
        </Step>

        <Step n={3} title="Tools">
          <ul className="divide-y divide-line rounded-md border border-line bg-panel">
            {TOOLS.map(([tool, desc]) => (
              <li key={tool} className="flex flex-wrap gap-x-3 px-3 py-2 text-sm">
                <code className="font-mono text-fg">{tool}</code>
                <span className="text-muted">{desc}</span>
              </li>
            ))}
          </ul>
        </Step>

        <Step n={4} title="Try it">
          <CopyBlock
            label="example prompt"
            text="Use terrarium to run a codex session on octocat/Hello-World that explains the repository, wait for it, and summarise the result."
          />
        </Step>
      </main>
    </div>
  );
}

function Step({ n, title, children }: { n: number; title: string; children: ReactNode }) {
  return (
    <section className="flex flex-col gap-2">
      <h2 className="flex items-center gap-2 text-sm font-semibold text-fg">
        <span className="flex h-5 w-5 items-center justify-center rounded-full bg-panel2 font-mono text-[11px] text-muted">
          {n}
        </span>
        {title}
      </h2>
      {children}
    </section>
  );
}
