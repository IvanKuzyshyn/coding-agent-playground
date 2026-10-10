import { redirect } from "next/navigation";

// Sessions open in a drawer over the runs list.
export default async function SessionPage({ params }: PageProps<"/sessions/[id]">) {
  const { id } = await params;
  redirect(`/?session=${encodeURIComponent(id)}`);
}
