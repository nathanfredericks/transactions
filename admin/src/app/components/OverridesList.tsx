"use client";
import { Alert, ListGroup } from "react-bootstrap";
import Link from "next/link";
import type { Override } from "@/app/types";
import { DeleteOverrideButton } from "@/app/overrides/[override]/edit/components/DeleteOverrideButton";

type Props = {
  overrides: Override[];
};

export default function OverridesList(props: Props) {
  const { overrides } = props;

  return (
    <>
      {overrides.length === 0 && (
        <Alert variant="light">
          No transaction rules yet. Create a rule to set a payee, category or
          memo.
        </Alert>
      )}
      <ListGroup>
        {overrides.map(({ id, name, revision }) => (
          <ListGroup.Item
            className="d-flex justify-content-between align-items-center flex-wrap gap-3 p-3"
            key={id}
          >
            <Link className="fw-medium" href={`/overrides/${id}/edit`}>
              {name}
            </Link>
            <div className="d-inline-flex gap-2">
              <Link
                className="btn btn-outline-secondary"
                aria-label={`Edit ${name}`}
                href={`/overrides/${id}/edit`}
              >
                Edit
              </Link>
              <DeleteOverrideButton id={id} revision={revision} />
            </div>
          </ListGroup.Item>
        ))}
      </ListGroup>
    </>
  );
}
