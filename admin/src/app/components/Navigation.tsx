"use client";
import { Container, Navbar } from "react-bootstrap";
import Link from "next/link";

export default function Navigation() {
  return (
    <Navbar className="bg-body-tertiary">
      <Container>
        <Navbar.Brand as={Link} href="/">
          Transactions
        </Navbar.Brand>
        <Link href="/jobs">Jobs and reviews</Link>
      </Container>
    </Navbar>
  );
}
