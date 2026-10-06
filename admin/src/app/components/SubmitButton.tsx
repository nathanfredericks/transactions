"use client";
import { useFormStatus } from "react-dom";
import { Button, Spinner } from "react-bootstrap";
import type { ReactNode } from "react";

export default function SubmitButton({
  children,
  variant = "primary",
}: {
  children: ReactNode;
  variant?: string;
}) {
  const { pending } = useFormStatus();
  return (
    <Button
      type="submit"
      variant={variant}
      disabled={pending}
      aria-busy={pending}
    >
      {pending && <Spinner size="sm" className="me-2" aria-hidden="true" />}
      {pending ? "Working…" : children}
    </Button>
  );
}
