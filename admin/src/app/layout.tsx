import type { Metadata } from "next";
import "bootstrap/dist/css/bootstrap.min.css";
import { Container } from "react-bootstrap";
import React from "react";
import Navigation from "@/app/components/Navigation";
import { appBaseUrl, auth0, isAdmin } from "@/lib/auth0";
import "./styles.css";

export const metadata: Metadata = {
  title: "Transactions",
};

export default async function RootLayout({
  children,
}: Readonly<{
  children: React.ReactNode;
}>) {
  const session = await auth0.getSession();
  return (
    <html lang="en" data-scroll-behavior="smooth">
      <body>
        <a className="visually-hidden-focusable skip-link" href="#main-content">
          Skip to content
        </a>
        <Navigation
          authorized={isAdmin(session?.user)}
          signedIn={!!session}
          logoutUrl={`/auth/logout?returnTo=${encodeURIComponent(`${appBaseUrl}/sign-in`)}`}
        />
        <main id="main-content" tabIndex={-1}>
          <Container className="d-flex flex-column gap-4 py-4 py-lg-5">
            {children}
          </Container>
        </main>
      </body>
    </html>
  );
}
