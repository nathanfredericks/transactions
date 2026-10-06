import { backend } from "../../utils/backend";
import { redirect } from "next/navigation";
import Link from "next/link";
import { Alert, Badge, Card, CardBody, Col, Row } from "react-bootstrap";
import SubmitButton from "../../components/SubmitButton";
import StatusBadge from "../../components/StatusBadge";
import {
  atlanticDate,
  displayLabel,
  type BankSummary,
} from "../../utils/display";
export const dynamic = "force-dynamic";
type Result = {
  job: {
    jobId: string;
    bank: string;
    status: string;
    error?: string;
    operation?: string;
    purpose: string;
    receivedAt: string;
    dryRun: boolean;
    snapshotKey?: string;
  };
  notifications: {
    jobId: string;
    id: string;
    title: string;
    message: string;
    status: string;
    attempts: number;
  }[];
};
export default async function JobPage({
  params,
  searchParams,
}: {
  params: Promise<{ job: string }>;
  searchParams: Promise<{ bank?: string; error?: string }>;
}) {
  const { job: jobId } = await params;
  const { bank, error } = await searchParams;
  if (!bank)
    return (
      <Alert variant="warning">
        Open this job from <Link href="/jobs">Bank activity</Link> to identify
        its bank.
      </Alert>
    );
  const [result, banks] = await Promise.all([
    backend<Result>("job.get", undefined, { bank, jobId }),
    backend<BankSummary[]>("banks.list"),
  ]);
  const name =
    banks.find((b) => b.bank === bank)?.name ?? "Purchase notification";
  const notices = (result.notifications ?? []).filter((n) => n.jobId === jobId);
  const undelivered = notices.some((n) =>
    ["failed", "pending"].includes(n.status),
  );
  async function run(action: string) {
    "use server";
    let failed = false;
    try {
      await backend(action, undefined, { bank: bank!, jobId });
    } catch {
      failed = true;
    }
    redirect(`/jobs/${jobId}?bank=${bank}${failed ? "&error=operation" : ""}`);
  }
  const retry = run.bind(null, "notifications.retry");
  const retryJob = run.bind(null, "job.retry");
  return (
    <>
      <Link href="/jobs">← Bank activity</Link>
      <header className="page-heading">
        <div>
          <h1>{name}</h1>
          <p>
            {displayLabel(result.job.purpose)} ·{" "}
            {atlanticDate(result.job.receivedAt)} Atlantic
          </p>
        </div>
        <div className="d-flex gap-2 align-items-center">
          <StatusBadge status={result.job.status} />
          {result.job.dryRun && (
            <Badge bg="secondary-subtle" text="secondary-emphasis">
              Dry run
            </Badge>
          )}
        </div>
      </header>
      {error && (
        <Alert variant="danger">
          The operation failed. Refresh before retrying.
        </Alert>
      )}
      {result.job.error && (
        <Alert
          variant={result.job.status === "complete" ? "warning" : "danger"}
        >
          <h2>{displayLabel(result.job.error)}</h2>
          <p className="mb-0">
            This is the result recorded for this job. Check the bank’s current
            status before taking action.
          </p>
        </Alert>
      )}
      <Card>
        <CardBody>
          <Row as="dl" className="gy-3 mb-3">
            <Col md={8}>
              <dt>Job reference</dt>
              <dd className="job-id mb-0">{jobId}</dd>
            </Col>
            <Col md={4}>
              <dt>Issue</dt>
              <dd className="mb-0">
                {result.job.operation === "previous-import-missing" ||
                result.job.operation === "purchase-not-found"
                  ? displayLabel(result.job.operation)
                  : result.job.error
                    ? displayLabel(result.job.error)
                    : "None"}
              </dd>
            </Col>
          </Row>
          {banks.some((b) => b.bank === bank) && (
            <Link href={`/banks/${bank}`}>Bank status and authentication</Link>
          )}
          {["failed", "review-required", "paused"].includes(
            result.job.status,
          ) && (
            <form action={retryJob} className="mt-3">
              <SubmitButton variant="outline-primary">
                Retry this job after resolving its issue
              </SubmitButton>
            </form>
          )}
          {result.job.dryRun && result.job.snapshotKey && (
            <div className="mt-3">
              <Link
                className="btn btn-outline-primary"
                href={`/jobs/${jobId}/baseline?bank=${bank}`}
              >
                Review starting point
              </Link>
            </div>
          )}
        </CardBody>
      </Card>
      <section aria-labelledby="notifications-heading">
        <h2 id="notifications-heading" className="mb-3">
          Notifications
        </h2>
        <div className="d-flex flex-column gap-3">
          {notices.map((n) => (
            <Card as="article" key={n.id}>
              <CardBody>
                <div className="page-heading mb-2">
                  <h3 className="mb-0">{n.title}</h3>
                  <StatusBadge status={n.status} />
                </div>
                <p>{n.message}</p>
                <p className="small text-body-secondary mb-0">
                  Delivery attempts: {n.attempts}
                </p>
              </CardBody>
            </Card>
          ))}
          {notices.length === 0 && (
            <Card>
              <CardBody className="text-body-secondary">
                No notifications for this job. Routine successful checks stay
                quiet.
              </CardBody>
            </Card>
          )}
        </div>
        {undelivered && (
          <form action={retry} className="mt-3">
            <SubmitButton variant="outline-secondary">
              Retry undelivered notifications
            </SubmitButton>
            <p className="small text-body-secondary mt-2">
              This only retries notification delivery. It does not import
              transactions.
            </p>
          </form>
        )}
      </section>
    </>
  );
}
