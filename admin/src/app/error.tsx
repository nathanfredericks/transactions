"use client";
import { Alert, Button } from "react-bootstrap";
export default function ErrorPage({ reset }: { reset: () => void }) {
  return (
    <Alert variant="danger" role="alert">
      <p>
        Unable to load transaction rules. Check the service configuration and
        try again.
      </p>
      <Button onClick={reset}>Try again</Button>
    </Alert>
  );
}
