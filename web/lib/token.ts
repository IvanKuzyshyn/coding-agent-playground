// Shown in place of the API token; copy actions substitute the real token.
export const TOKEN_MASK = "••••••••";

export async function fetchToken(): Promise<string> {
  const res = await fetch("/api/token", { cache: "no-store" });
  if (!res.ok) throw new Error(`token: HTTP ${res.status}`);
  return ((await res.json()) as { token: string }).token;
}

// copyWithToken copies text with TOKEN_MASK replaced by the real token. The clipboard write starts
// synchronously with a pending value, so browsers that require a direct user gesture (Safari)
// still allow it after the fetch.
export async function copyWithToken(text: string): Promise<void> {
  const blob = fetchToken().then((t) => new Blob([text.replaceAll(TOKEN_MASK, t)], { type: "text/plain" }));
  await navigator.clipboard.write([new ClipboardItem({ "text/plain": blob })]);
}
