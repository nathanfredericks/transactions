"use client";

import Link from "next/link";
import { Nav, Pagination } from "react-bootstrap";

export function JobFilters({ scope }: { scope: string }) {
  return (
    <Nav as="nav" variant="pills" aria-label="Filter jobs">
      {[
        ["all", "All jobs"],
        ["live", "Live"],
        ["dry", "Dry runs"],
      ].map(([key, label]) => (
        <Nav.Item key={key}>
          <Nav.Link
            as={Link}
            active={scope === key}
            aria-current={scope === key ? "page" : undefined}
            href={`/jobs?scope=${key}`}
          >
            {label}
          </Nav.Link>
        </Nav.Item>
      ))}
    </Nav>
  );
}

export function JobPagination({
  scope,
  page,
  pages,
}: {
  scope: string;
  page: number;
  pages: number;
}) {
  return (
    <Pagination className="mb-0">
      <Pagination.Item
        as={Link}
        href={`/jobs?scope=${scope}&page=${Math.max(1, page - 1)}`}
        disabled={page === 1}
      >
        Newer
      </Pagination.Item>
      <Pagination.Item active aria-current="page">
        {page}
      </Pagination.Item>
      <Pagination.Item
        as={Link}
        href={`/jobs?scope=${scope}&page=${Math.min(pages, page + 1)}`}
        disabled={page === pages}
      >
        Older
      </Pagination.Item>
    </Pagination>
  );
}
