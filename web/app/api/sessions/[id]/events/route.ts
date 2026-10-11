import { apiRaw } from "@/lib/api";
import { getAuth } from "@/lib/auth";

export const dynamic = "force-dynamic";

// Proxies the API's SSE stream so the browser never holds the engineer token.
export async function GET(req: Request, ctx: RouteContext<"/api/sessions/[id]/events">) {
  const auth = await getAuth();
  if (!auth) return Response.json({ error: "unauthorized" }, { status: 401 });

  const { id } = await ctx.params;
  const after = new URL(req.url).searchParams.get("after");
  const lastEventId = req.headers.get("last-event-id");
  const headers: Record<string, string> = { Accept: "text/event-stream" };
  if (lastEventId) headers["Last-Event-ID"] = lastEventId;
  const qs = after && !lastEventId ? `?after=${encodeURIComponent(after)}` : "";

  let upstream: Response;
  try {
    upstream = await apiRaw(auth.token, `/sessions/${encodeURIComponent(id)}/events${qs}`, {
      headers,
      signal: req.signal,
    });
  } catch {
    return Response.json({ error: "API unreachable" }, { status: 502 });
  }
  if (!upstream.ok || !upstream.body) {
    const body = await upstream.text();
    return new Response(body, {
      status: upstream.status,
      headers: { "Content-Type": upstream.headers.get("Content-Type") ?? "text/plain" },
    });
  }
  return new Response(upstream.body, {
    headers: {
      "Content-Type": "text/event-stream",
      "Cache-Control": "no-cache, no-transform",
      Connection: "keep-alive",
      "X-Accel-Buffering": "no",
    },
  });
}
