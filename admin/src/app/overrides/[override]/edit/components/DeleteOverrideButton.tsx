"use client";

import { useState } from "react";
import { Alert, Button } from "react-bootstrap";
import { deleteOverride } from "@/app/actions";

type Props = {
  id: string;
  revision: string;
};

export function DeleteOverrideButton(props: Props) {
  const { id } = props;

  const [error, setError] = useState("");
  const [pending, setPending] = useState(false);
  return (
    <div>
      {error && (
        <Alert variant="danger" role="alert">
          {error}
        </Alert>
      )}
      <Button
        disabled={pending}
        onClick={async () => {
          setPending(true);
          setError("");
          try {
            const result = await deleteOverride(id, props.revision);
            if (result?.error) setError(result.error);
          } catch {
            setError("Unable to delete override. Please try again.");
          } finally {
            setPending(false);
          }
        }}
        variant="danger"
      >
        Delete
      </Button>
    </div>
  );
}
