import LinkButton from "./components/LinkButton";
import { Card, CardBody } from "react-bootstrap";
export default function NotFound() {
  return (
    <Card>
      <CardBody className="py-5 text-center">
        <h1>Page not found</h1>
        <p className="text-body-secondary">
          This page may have moved or no longer exists.
        </p>
        <LinkButton href="/jobs">Go to bank activity</LinkButton>
      </CardBody>
    </Card>
  );
}
