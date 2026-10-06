import type { Metadata } from "next";
import "bootstrap/dist/css/bootstrap.min.css";
import { Container } from "react-bootstrap";
import React from "react";
import Navigation from "@/app/components/Navigation";
import "./styles.css";

export const metadata: Metadata = {
  title: "Transactions",
};

export default function RootLayout({
  children,
}: Readonly<{
  children: React.ReactNode;
}>) {
  return (
    <html lang="en" data-scroll-behavior="smooth">
      <body>
        <a className="visually-hidden-focusable skip-link" href="#main-content">
          Skip to content
        </a>
        <Navigation />
        <main id="main-content">
          <Container className="d-flex flex-column gap-4 py-4 py-lg-5">
            {children}
          </Container>
        </main>
      </body>
    </html>
  );
}
