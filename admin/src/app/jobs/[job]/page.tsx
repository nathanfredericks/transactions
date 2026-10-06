import { backend } from "../../utils/backend";
import { redirect } from "next/navigation";
import Link from "next/link";
export const dynamic = "force-dynamic";
type Result = {
  job: {
    jobId: string;
    bank: string;
    status: string;
    error?: string;
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
      <p role="alert">Open this job from Bank activity to identify its bank.</p>
    );
  const result = await backend<Result>("job.get", undefined, { bank, jobId });
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
      {error && (
        <p role="alert">The operation failed. Refresh before retrying.</p>
      )}
      <h1>{result.job.bank}</h1>
      <dl>
        <dt>Status</dt>
        <dd>{result.job.status}</dd>
        <dt>Issue</dt>
        <dd>{result.job.error ?? "None"}</dd>
        <dt>Job</dt>
        <dd>{jobId}</dd>
      </dl>
      <p>
        <Link href={`/banks/${bank}`}>Bank settings and authentication</Link>
      </p>
      {["failed", "review-required", "paused"].includes(result.job.status) && (
        <form action={retryJob}>
          <button type="submit">
            Retry this job after resolving its issue
          </button>
        </form>
      )}
      {result.job.dryRun && result.job.snapshotKey && (
        <p>
          <Link href={`/jobs/${jobId}/baseline?bank=${bank}`}>
            Review the baseline from this dry run
          </Link>
        </p>
      )}
      <h2>Notifications</h2>
      {result.notifications
        .filter((n) => n.jobId === jobId)
        .map((n) => (
          <article key={n.id}>
            <h3>{n.title}</h3>
            <p>{n.message}</p>
            <p>
              Delivery: {n.status} · Attempts: {n.attempts}
            </p>
          </article>
        ))}
      <form action={retry}>
        <button className="btn btn-secondary" type="submit">
          Retry undelivered notifications
        </button>
      </form>
      <p>This only retries notification delivery.</p>
    </>
  );
}
