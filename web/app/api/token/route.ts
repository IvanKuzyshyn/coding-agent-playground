import { getAuth } from "@/lib/auth";

export const dynamic = "force-dynamic";

// Hands the API token to the browser only when the user asks to copy it; pages never embed it.
export async function GET() {
  const auth = await getAuth();
  if (!auth) return Response.json({ error: "unauthorized" }, { status: 401 });
  return Response.json({ token: auth.token }, { headers: { "Cache-Control": "no-store" } });
}
