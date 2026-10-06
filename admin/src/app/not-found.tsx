import Link from "next/link";
import { Card, CardBody } from "react-bootstrap";
export default function NotFound() {
  return (
    <Card>
      <CardBody className="py-5 text-center">
        <h1>Page not found</h1>
        <p className="text-body-secondary">
          This page may have moved or no longer exists.
        </p>
        <Link className="btn btn-primary" href="/jobs">
          Go to bank activity
        </Link>
      </CardBody>
    </Card>
  );
}
