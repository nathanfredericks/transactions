import "server-only";
import { redirect } from "next/navigation";
import { auth0, isAdmin } from "./auth0";

export async function requireAdmin() {
  const session = await auth0.getSession();
  if (!session) redirect("/auth/login");
  if (!isAdmin(session.user)) redirect("/sign-in?error=access");
  return session.user;
}
