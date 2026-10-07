import { Auth0Client } from "@auth0/nextjs-auth0/server";
import { NextResponse } from "next/server";

export const appBaseUrl =
  process.env.APP_BASE_URL || "https://transactions-admin.fredericks.app";

export function isAdmin(user?: { email?: string; email_verified?: boolean }) {
  const allowed = (process.env.AUTH0_ALLOWED_EMAILS || "")
    .split(",")
    .map((email) => email.trim().toLowerCase())
    .filter(Boolean);
  return (
    !!user?.email &&
    user.email_verified === true &&
    allowed.includes(user.email.toLowerCase())
  );
}

export const auth0 = new Auth0Client({
  appBaseUrl,
  enableAccessTokenEndpoint: false,
  authorizationParameters: { scope: "openid profile email" },
  session: {
    rolling: true,
    absoluteDuration: 86400,
    inactivityDuration: 86400,
  },
  async onCallback(error, context, session) {
    if (error) {
      return NextResponse.redirect(new URL("/sign-in?error=login", appBaseUrl));
    }
    if (!isAdmin(session?.user)) {
      return NextResponse.redirect(
        new URL("/sign-in?error=access", appBaseUrl),
      );
    }
    return NextResponse.redirect(new URL(context.returnTo || "/", appBaseUrl));
  },
});
