import Link from "next/link";
import { redirect } from "next/navigation";
import { backend } from "../../utils/backend";
export const dynamic = "force-dynamic";
type Bank = {
  bank: string;
  name: string;
  enabled: boolean;
  baselineApproved: boolean;
  health: { blocked: boolean; kind: string };
};
export default async function BankPage({
  params,
  searchParams,
}: {
  params: Promise<{ bank: string }>;
  searchParams: Promise<{ error?: string; job?: string }>;
}) {
  const { bank } = await params;
  const query = await searchParams;
  const banks = await backend<Bank[]>("banks.list");
  const current = banks.find((b) => b.bank === bank);
  if (!current) return <p role="alert">Unknown bank.</p>;
  async function resume() {
    "use server";
    let failed = false;
    try {
      await backend("bank.resume", undefined, { bank, jobId: "" });
    } catch {
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
    } catch {
      failed = true;
    }
    redirect(
      failed ? `/banks/${bank}?error=dry-run` : `/jobs/${jobId}?bank=${bank}`,
    );
  }
  return (
    <>
      <h1>{current.name}</h1>
      {query.error && (
        <p role="alert">
          The operation failed. Refresh and inspect the bank status before
          retrying.
        </p>
      )}
      <dl>
        <dt>Authentication</dt>
        <dd>{current.health.blocked ? "Paused for review" : "Automatic"}</dd>
        <dt>Current issue</dt>
        <dd>{current.health.kind || "None"}</dd>
        <dt>Baseline</dt>
        <dd>{current.baselineApproved ? "Approved" : "Review required"}</dd>
      </dl>
      <form action={dryRun}>
        <button type="submit" className="btn btn-primary">
          Fetch a dry run
        </button>
      </form>
      <p>
        A dry run reads bank and YNAB data without importing or sending purchase
        notifications.
      </p>
      {current.health.blocked && (
        <form action={resume}>
          <p>
            Update rejected credentials or resolve the authentication challenge
            first. This does not clear uncertain financial writes.
          </p>
          <button type="submit" className="btn btn-warning">
            Resume automatic authentication
          </button>
        </form>
      )}
      <Link href="/jobs">Return to activity</Link>
    </>
  );
}
