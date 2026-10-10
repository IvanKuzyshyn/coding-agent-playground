import { redirect } from "next/navigation";

// The runs list moved to the home page.
export default function SessionsPage() {
  redirect("/");
}
