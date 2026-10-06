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
    <html lang="en">
      <body>
        <Navigation />
        <main>
          <Container className="d-flex flex-column gap-2 mt-2 mb-4">
            {children}
          </Container>
        </main>
      </body>
    </html>
  );
}
