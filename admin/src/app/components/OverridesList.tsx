"use client";
import { Button, ListGroup } from "react-bootstrap";
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
      {overrides.length === 0 && <p>No overrides</p>}
      <ListGroup>
        {overrides.map(({ id, name, revision }) => (
          <ListGroup.Item
            className="d-flex justify-content-between align-items-center"
            key={id}
          >
            <div className="d-flex flex-column">{name}</div>
            <div className="d-inline-flex gap-2">
              <Link href={`/overrides/${id}/edit`}>
                <Button variant="secondary">Edit</Button>
              </Link>
              <DeleteOverrideButton id={id} revision={revision} />
            </div>
          </ListGroup.Item>
        ))}
      </ListGroup>
    </>
  );
}
