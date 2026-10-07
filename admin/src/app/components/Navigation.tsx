"use client";
import { Container, Nav, Navbar } from "react-bootstrap";
import Link from "next/link";
import { usePathname } from "next/navigation";

export default function Navigation({
  authorized,
  signedIn,
  logoutUrl,
}: {
  authorized: boolean;
  signedIn: boolean;
  logoutUrl: string;
}) {
  const path = usePathname();
  const activity = path.startsWith("/jobs") || path.startsWith("/banks");
  return (
    <Navbar
      expand="sm"
      className="bg-white border-bottom py-3"
      aria-label="Main navigation"
    >
      <Container>
        <Navbar.Brand as={Link} href="/">
          Transactions
        </Navbar.Brand>
        <Navbar.Toggle aria-controls="main-navigation" />
        <Navbar.Collapse id="main-navigation">
          <Nav className="ms-auto gap-sm-2">
            {authorized && (
              <>
                <Nav.Link
                  as={Link}
                  href="/jobs"
                  active={activity}
                  aria-current={activity ? "page" : undefined}
                >
                  Bank activity
                </Nav.Link>
                <Nav.Link
                  as={Link}
                  href="/"
                  active={!activity}
                  aria-current={!activity ? "page" : undefined}
                >
                  Transaction rules
                </Nav.Link>
              </>
            )}
            <Nav.Link href={signedIn ? logoutUrl : "/auth/login"}>
              {signedIn ? "Sign out" : "Sign in"}
            </Nav.Link>
          </Nav>
        </Navbar.Collapse>
      </Container>
    </Navbar>
  );
}
