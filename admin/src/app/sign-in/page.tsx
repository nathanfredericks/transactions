import {
  Alert,
  Button,
  Card,
  CardBody,
  CardHeader,
  Col,
  Row,
} from "react-bootstrap";
import { redirect } from "next/navigation";
import { appBaseUrl, auth0, isAdmin } from "@/lib/auth0";

export const metadata = { title: "Sign in | Transactions" };

export default async function SignIn({
  searchParams,
}: {
  searchParams: Promise<{ error?: string }>;
}) {
  const session = await auth0.getSession();
  if (isAdmin(session?.user)) redirect("/");
  const { error } = await searchParams;
  const logout = `/auth/logout?returnTo=${encodeURIComponent(`${appBaseUrl}/sign-in`)}`;
  return (
    <Row className="justify-content-center">
      <Col md={8} lg={6}>
        <Card>
          <CardHeader as="h1" className="h4">
            Sign in to Transactions
          </CardHeader>
          <CardBody>
            <p className="text-body-secondary">
              Use your account to manage bank activity and transaction rules.
            </p>
            {error === "login" && (
              <Alert variant="warning">
                Sign-in couldn’t be completed. Please try again.
              </Alert>
            )}
            {(error === "access" || session) && (
              <Alert variant="warning">
                This account doesn’t have access. Sign in with your approved,
                verified email address.
              </Alert>
            )}
            <Button href={session ? logout : "/auth/login"}>
              {session ? "Sign out and switch account" : "Sign in"}
            </Button>
          </CardBody>
        </Card>
      </Col>
    </Row>
  );
}
