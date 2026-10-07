import Link from "next/link";
import { redirect, unstable_rethrow } from "next/navigation";
import { backend } from "../../utils/backend";
import { Alert, Card, CardBody, Col, Row } from "react-bootstrap";
import SubmitButton from "../../components/SubmitButton";
import { displayLabel, type BankSummary } from "../../utils/display";
export const dynamic = "force-dynamic";
export default async function BankPage({
  params,
  searchParams,
}: {
  params: Promise<{ bank: string }>;
  searchParams: Promise<{ error?: string; job?: string }>;
}) {
  const { bank } = await params;
  const query = await searchParams;
  const banks = await backend<BankSummary[]>("banks.list");
  const current = banks.find((b) => b.bank === bank);
  if (!current)
    return (
      <Alert variant="warning">
        Unknown bank. <Link href="/jobs">Return to bank activity</Link>.
      </Alert>
    );
  async function resume() {
    "use server";
    let failed = false;
    try {
      await backend("bank.resume", undefined, { bank, jobId: "" });
    } catch (error) {
      unstable_rethrow(error);
      failed = true;
    }
    redirect(
      `/banks/${encodeURIComponent(bank)}${failed ? "?error=resume" : ""}`,
    );
  }
  async function dryRun() {
    "use server";
    const jobId = `review-${bank}-${crypto.randomUUID()}`;
    let failed = false;
    try {
      await backend("submit", undefined, undefined, {
        version: 1,
        jobId,
        bank,
        source: "manual",
        purpose: "retrieve",
        dryRun: true,
      });
    } catch (error) {
      unstable_rethrow(error);
      failed = true;
    }
    redirect(
      failed ? `/banks/${bank}?error=dry-run` : `/jobs/${jobId}?bank=${bank}`,
    );
  }
  return (
    <>
      <Link href="/jobs">← Bank activity</Link>
      <header className="page-heading">
        <div>
          <h1>{current.name}</h1>
          <p>Authentication, imports and starting-point review.</p>
        </div>
      </header>
      {query.error && (
        <Alert variant="danger">
          The operation failed. Refresh and inspect the bank status before
          retrying.
        </Alert>
      )}
      <Card>
        <CardBody>
          <Row as="dl" className="mb-0 gy-3">
            <Col sm={4}>
              <dt>Authentication</dt>
              <dd className="mb-0">
                {current.health.blocked ? "Paused for review" : "Automatic"}
              </dd>
            </Col>
            <Col sm={4}>
              <dt>Current issue</dt>
              <dd className="mb-0">
                {current.health.kind
                  ? displayLabel(current.health.kind)
                  : "None"}
                {current.health.importReview && (
                  <div className="text-danger">
                    Imports are held for review.
                  </div>
                )}
              </dd>
            </Col>
            <Col sm={4}>
              <dt>Starting point</dt>
              <dd className="mb-0">
                {current.baselineApproved ? "Approved" : "Review required"}
              </dd>
            </Col>
          </Row>
        </CardBody>
      </Card>
      <Card>
        <CardBody>
          <h2>Check bank data</h2>
          <p className="text-body-secondary">
            A dry run reads bank and YNAB data without importing or sending
            purchase notifications.
          </p>
          <form action={dryRun}>
            <SubmitButton>Fetch a dry run</SubmitButton>
          </form>
        </CardBody>
      </Card>
      {current.health.blocked && (
        <Alert variant="warning">
          <h2>Resume sign-in</h2>
          <p>
            Update rejected credentials or resolve the authentication challenge
            first. This does not clear uncertain financial writes.
          </p>
          <form action={resume}>
            <SubmitButton variant="warning">
              Resume automatic authentication
            </SubmitButton>
          </form>
        </Alert>
      )}
    </>
  );
}
