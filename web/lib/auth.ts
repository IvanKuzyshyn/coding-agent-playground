import "server-only";

import { createHash } from "node:crypto";
import { EncryptJWT, jwtDecrypt } from "jose";
import { cookies } from "next/headers";
import { redirect } from "next/navigation";

const COOKIE = "terrarium_session";
const MAX_AGE_S = 7 * 24 * 60 * 60;
const DEV_SECRET = "terrarium-dev-only-secret-do-not-use-in-production";

export interface Auth {
  token: string;
  name: string;
}

let key: Uint8Array | undefined;

function secretKey(): Uint8Array {
  if (key) return key;
  let secret = process.env.TERRARIUM_WEB_SECRET;
  if (!secret) {
    if (process.env.NODE_ENV === "production") {
      throw new Error("TERRARIUM_WEB_SECRET must be set in production");
    }
    secret = DEV_SECRET;
  }
  // A256GCM needs exactly 32 bytes; hashing accepts secrets of any length.
  key = new Uint8Array(createHash("sha256").update(secret).digest());
  return key;
}

// Secure cookies are not stored over plain http, so allow opting out for http-only setups
// (e.g. a phone reaching the host over a private overlay without TLS).
function secureCookie(): boolean {
  return process.env.NODE_ENV === "production" && process.env.TERRARIUM_INSECURE_COOKIE !== "1";
}

export async function setAuth(auth: Auth): Promise<void> {
  const jwt = await new EncryptJWT({ token: auth.token, name: auth.name })
    .setProtectedHeader({ alg: "dir", enc: "A256GCM" })
    .setIssuedAt()
    .setExpirationTime(`${MAX_AGE_S}s`)
    .encrypt(secretKey());
  (await cookies()).set(COOKIE, jwt, {
    httpOnly: true,
    sameSite: "lax",
    secure: secureCookie(),
    path: "/",
    maxAge: MAX_AGE_S,
  });
}

export async function clearAuth(): Promise<void> {
  (await cookies()).delete(COOKIE);
}

export async function getAuth(): Promise<Auth | null> {
  const raw = (await cookies()).get(COOKIE)?.value;
  if (!raw) return null;
  try {
    const { payload } = await jwtDecrypt(raw, secretKey());
    if (typeof payload.token !== "string" || typeof payload.name !== "string") return null;
    return { token: payload.token, name: payload.name };
  } catch {
    return null;
  }
}

export async function requireAuth(): Promise<Auth> {
  const auth = await getAuth();
  if (!auth) redirect("/login");
  return auth;
}
