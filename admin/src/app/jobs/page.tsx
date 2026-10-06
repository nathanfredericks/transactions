import Link from "next/link";
import { Badge, Card, CardBody, Col, Row, Table } from "react-bootstrap";
import { backend } from "../utils/backend";
import { atlanticDate, displayLabel, type BankSummary } from "../utils/display";
import StatusBadge from "../components/StatusBadge";
export const dynamic = "force-dynamic";
type Job = {
  jobId: string;
  bank: string;
  status: string;
  purpose: string;
  receivedAt: string;
  error?: string;
  dryRun: boolean;
};
export default async function Jobs({
  searchParams,
}: {
  searchParams: Promise<{ scope?: string; page?: string }>;
}) {
  const query = await searchParams;
  const scope = ["live", "dry"].includes(query.scope ?? "")
    ? query.scope!
    : "all";
  const [banks, result] = await Promise.all([
    backend<BankSummary[]>("banks.list"),
    backend<Job[] | null>("jobs.list"),
  ]);
  const jobs = (result ?? []).filter(
    (j) => scope === "all" || j.dryRun === (scope === "dry"),
  );
  jobs.sort((a, b) => b.receivedAt.localeCompare(a.receivedAt));
  const pages = Math.max(1, Math.ceil(jobs.length / 50));
  const page = Math.min(
    pages,
    Math.max(1, Math.floor(Number(query.page) || 1)),
  );
  return (
    <>
      <header className="page-heading">
        <div>
          <h1>Bank activity</h1>
          <p>Current bank status, imports and items needing review.</p>
        </div>
      </header>
      <Row as="nav" aria-label="Banks" className="g-3">
        {banks.map((b) => (
          <Col md={4} key={b.bank}>
            <Card className="h-100">
              <CardBody>
                <h2>
                  <Link href={`/banks/${b.bank}`}>{b.name}</Link>
                </h2>
                <Badge
                  bg={
                    b.health.blocked || b.health.importReview
                      ? "danger-subtle"
                      : "secondary-subtle"
                  }
                  text={
                    b.health.blocked || b.health.importReview
                      ? "danger-emphasis"
                      : "secondary-emphasis"
                  }
                  className="status-badge"
                >
                  {b.health.blocked
                    ? "Sign-in paused"
                    : b.health.importReview
                      ? "Imports need review"
                      : !b.enabled
                        ? "Disabled"
                        : b.health.kind
                          ? displayLabel(b.health.kind)
                          : "No current issues"}
                </Badge>
                <p className="small text-body-secondary mt-3 mb-0">
                  {b.baselineApproved
                    ? "Starting point approved"
                    : "Starting point needs review"}
                </p>
              </CardBody>
            </Card>
          </Col>
        ))}
      </Row>
      <section aria-labelledby="recent-jobs">
        <div className="page-heading mb-3">
          <h2 id="recent-jobs" className="mb-0">
            Recent jobs
          </h2>
          <nav className="nav nav-pills gap-1" aria-label="Filter jobs">
            {[
              ["all", "All jobs"],
              ["live", "Live"],
              ["dry", "Dry runs"],
            ].map(([key, label]) => (
              <Link
                key={key}
                className={`nav-link ${scope === key ? "active" : ""}`}
                aria-current={scope === key ? "page" : undefined}
                href={`/jobs?scope=${key}`}
              >
                {label}
              </Link>
            ))}
          </nav>
        </div>
        <Card>
          <Table responsive hover className="mb-0 align-middle">
            <caption>
              Times are Atlantic. Past failures remain in the history after
              recovery. Dry runs do not import.
            </caption>
            <thead className="table-light">
              <tr>
                <th scope="col">Received</th>
                <th scope="col">Bank</th>
                <th scope="col">Purpose</th>
                <th scope="col">Result</th>
              </tr>
            </thead>
            <tbody>
              {jobs.slice((page - 1) * 50, page * 50).map((j) => (
                <tr key={`${j.bank}-${j.jobId}`}>
                  <td className="text-nowrap">
                    <Link
                      href={`/jobs/${encodeURIComponent(j.jobId)}?bank=${encodeURIComponent(j.bank)}`}
                    >
                      {atlanticDate(j.receivedAt)}
                    </Link>
                  </td>
                  <td>
                    {banks.find((b) => b.bank === j.bank)?.name ??
                      "Purchase notification"}
                  </td>
                  <td>
                    {displayLabel(j.purpose)}
                    {j.dryRun && (
                      <Badge
                        bg="secondary-subtle"
                        text="secondary-emphasis"
                        className="ms-2"
                      >
                        Dry run
                      </Badge>
                    )}
                  </td>
                  <td>
                    <StatusBadge status={j.status} />
                    {j.error && (
                      <div className="small text-body-secondary mt-1">
                        {displayLabel(j.error)}
                      </div>
                    )}
                  </td>
                </tr>
              ))}
              {jobs.length === 0 && (
                <tr>
                  <td
                    colSpan={4}
                    className="text-center text-body-secondary py-5"
                  >
                    No jobs in this view yet.
                  </td>
                </tr>
              )}
            </tbody>
          </Table>
        </Card>
        <nav
          className="d-flex align-items-center justify-content-between mt-3"
          aria-label="Job pages"
        >
          {page > 1 ? (
            <Link
              className="btn btn-outline-secondary"
              href={`/jobs?scope=${scope}&page=${page - 1}`}
            >
              Newer
            </Link>
          ) : (
            <span />
          )}
          <span className="small text-body-secondary">
            Page {page} of {pages} · {jobs.length} jobs
          </span>
          {page < pages ? (
            <Link
              className="btn btn-outline-secondary"
              href={`/jobs?scope=${scope}&page=${page + 1}`}
            >
              Older
            </Link>
          ) : (
            <span />
          )}
        </nav>
      </section>
    </>
  );
}
