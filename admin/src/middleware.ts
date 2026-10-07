import { NextRequest, NextResponse } from "next/server";
import { appBaseUrl, auth0, isAdmin } from "@/lib/auth0";

export async function middleware(request: NextRequest) {
  const response = await auth0.middleware(request);
  const path = request.nextUrl.pathname;
  if (path.startsWith("/auth/") || path === "/sign-in") return response;

  const session = await auth0.getSession(request);
  if (!session) {
    const login = new URL("/auth/login", appBaseUrl);
    login.searchParams.set("returnTo", path + request.nextUrl.search);
    return NextResponse.redirect(login);
  }
  if (!isAdmin(session.user)) {
    return NextResponse.redirect(new URL("/sign-in?error=access", appBaseUrl));
  }
  return response;
}

export const config = {
  matcher: ["/((?!_next/static|_next/image|favicon.ico).*)"],
};
