import Link from "next/link";
import {
  Badge,
  Card,
  CardBody,
  CardHeader,
  Col,
  ListGroup,
  ListGroupItem,
  Row,
  Table,
} from "react-bootstrap";
import { backend } from "../utils/backend";
import {
  displayDate,
  displayTime,
  displayLabel,
  type BankSummary,
} from "../utils/display";
import StatusBadge from "../components/StatusBadge";
import AutoRefresh from "../components/AutoRefresh";
import BankHealthBadge from "../components/BankHealthBadge";
import { JobFilters, JobPagination } from "./components/JobNavigation";
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
  const visibleJobs = jobs.slice((page - 1) * 50, page * 50);
  return (
    <>
      <AutoRefresh />
      <header>
        <div>
          <h1>Bank activity</h1>
          <p className="text-body-secondary mb-0">
            Current bank status, imports and items needing review.
          </p>
        </div>
      </header>
      <ListGroup as="nav" aria-label="Banks" className="d-md-none">
        {banks.map((bank) => (
          <ListGroupItem key={bank.bank}>
            <div className="d-flex justify-content-between align-items-center flex-wrap gap-2">
              <h2 className="h6 mb-0">
                <Link href={`/banks/${bank.bank}`}>{bank.name}</Link>
              </h2>
              <BankHealthBadge bank={bank} />
            </div>
            <p className="small text-body-secondary mt-1 mb-0">
              {bank.baselineApproved
                ? "Starting point approved"
                : "Starting point needs review"}
            </p>
          </ListGroupItem>
        ))}
      </ListGroup>
      <Row as="nav" aria-label="Banks" className="g-3 d-none d-md-flex">
        {banks.map((b) => (
          <Col md={4} key={b.bank}>
            <Card className="h-100">
              <CardHeader as="h2" className="h5">
                <Link href={`/banks/${b.bank}`}>{b.name}</Link>
              </CardHeader>
              <CardBody>
                <BankHealthBadge bank={b} />
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
        <div className="d-flex justify-content-between align-items-center flex-wrap gap-3 mb-3">
          <h2 id="recent-jobs" className="mb-0">
            Recent jobs
          </h2>
          <JobFilters scope={scope} />
        </div>
        <ListGroup className="d-md-none">
          {visibleJobs.map((job) => (
            <ListGroupItem as="article" key={`${job.bank}-${job.jobId}`}>
              <div className="d-flex justify-content-between align-items-center flex-wrap gap-2 mb-2">
                <Link
                  className="small"
                  href={`/jobs/${encodeURIComponent(job.jobId)}?bank=${encodeURIComponent(job.bank)}`}
                >
                  <span className="d-block">{displayDate(job.receivedAt)}</span>
                  <span className="d-block">{displayTime(job.receivedAt)}</span>
                </Link>
                <StatusBadge status={job.status} />
              </div>
              <p className="mb-1">
                {banks.find((bank) => bank.bank === job.bank)?.name ??
                  "Purchase notification"}
              </p>
              <div className="d-flex align-items-center flex-wrap gap-2">
                <span className="small text-body-secondary">
                  {displayLabel(job.purpose)}
                </span>
                {job.dryRun && <Badge bg="secondary">Dry run</Badge>}
              </div>
              {job.error && (
                <p className="small text-body-secondary mt-2 mb-0">
                  {displayLabel(job.error)}
                </p>
              )}
            </ListGroupItem>
          ))}
          {jobs.length === 0 && (
            <ListGroupItem className="text-body-secondary">
              No jobs in this view yet.
            </ListGroupItem>
          )}
        </ListGroup>
        <p className="small text-body-secondary d-md-none mt-2 mb-0">
          Times are Atlantic. Past failures remain in the history after
          recovery. Dry runs do not import.
        </p>
        <Card className="d-none d-md-block">
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
              {visibleJobs.map((j) => (
                <tr key={`${j.bank}-${j.jobId}`}>
                  <td className="text-nowrap">
                    <Link
                      href={`/jobs/${encodeURIComponent(j.jobId)}?bank=${encodeURIComponent(j.bank)}`}
                    >
                      <span className="d-block">
                        {displayDate(j.receivedAt)}
                      </span>
                      <span className="d-block small text-body-secondary">
                        {displayTime(j.receivedAt)}
                      </span>
                    </Link>
                  </td>
                  <td>
                    {banks.find((b) => b.bank === j.bank)?.name ??
                      "Purchase notification"}
                  </td>
                  <td>
                    {displayLabel(j.purpose)}
                    {j.dryRun && (
                      <Badge bg="secondary" className="ms-2">
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
          className="d-flex align-items-center justify-content-between flex-wrap gap-3 mt-3"
          aria-label="Job pages"
        >
          <JobPagination scope={scope} page={page} pages={pages} />
          <span className="small text-body-secondary">
            Page {page} of {pages}
            <span className="d-block">{jobs.length} jobs</span>
          </span>
        </nav>
      </section>
    </>
  );
}
