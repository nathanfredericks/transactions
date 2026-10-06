"use client";
import { Alert, Button } from "react-bootstrap";
export default function ErrorPage({ reset }: { reset: () => void }) {
  return (
    <Alert variant="danger" role="alert">
      <p>
        This page could not load. Try again. If it still fails, check the
        service status.
      </p>
      <Button onClick={reset}>Try again</Button>
    </Alert>
  );
}
