"use client";
import { Container, Navbar } from "react-bootstrap";
import Link from "next/link";

export default function Navigation() {
  return (
    <Navbar className="bg-body-tertiary">
      <Container>
        <Link href="/" legacyBehavior passHref>
          <Navbar.Brand>Transactions</Navbar.Brand>
        </Link>
      <Link href="/jobs">Jobs and reviews</Link>
      </Container>
    </Navbar>
  );
}
