import { redirect } from "next/navigation";
import { getAuth } from "@/lib/auth";
import { LoginForm } from "./login-form";

export default async function LoginPage({ searchParams }: PageProps<"/login">) {
  const { expired } = await searchParams;
  if (!expired && (await getAuth())) redirect("/");
  return (
    <main className="mx-auto flex min-h-screen max-w-sm flex-col justify-center px-4">
      <h1 className="mb-1 font-mono text-lg text-fg">terrarium</h1>
      <p className="mb-6 text-sm text-muted">Enter a user name. A new name creates a new user.</p>
      {expired && (
        <p className="mb-4 rounded border border-amber-500/40 bg-amber-500/10 px-3 py-2 text-sm text-amber-200">
          Your session expired. Log in again.
        </p>
      )}
      <LoginForm />
    </main>
  );
}
