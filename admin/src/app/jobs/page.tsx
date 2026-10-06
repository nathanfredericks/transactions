import Link from "next/link";
import { backend } from "../utils/backend";
export const dynamic = "force-dynamic";
type Job = {
  jobId: string;
  bank: string;
  status: string;
  purpose: string;
  receivedAt: string;
  error?: string;
};
export default async function Jobs() {
  const banks = await backend<{ bank: string; name: string }[]>("banks.list");
  const jobs = (await backend<Job[] | null>("jobs.list")) ?? [];
  jobs.sort((a, b) => b.receivedAt.localeCompare(a.receivedAt));
  return (
    <>
      <h1>Bank activity</h1>
      <p>Imports, sign-ins and items needing review.</p>
      <nav aria-label="Banks">
        {banks.map((b) => (
          <p key={b.bank}>
            <Link href={`/banks/${b.bank}`}>{b.name}</Link>
          </p>
        ))}
      </nav>
      <table className="table">
        <caption>Recent bank jobs</caption>
        <thead>
          <tr>
            <th>Received</th>
            <th>Bank</th>
            <th>Purpose</th>
            <th>Status</th>
          </tr>
        </thead>
        <tbody>
          {jobs.map((j) => (
            <tr key={j.jobId}>
              <td>
                <Link
                  href={`/jobs/${encodeURIComponent(j.jobId)}?bank=${encodeURIComponent(j.bank)}`}
                >
                  {new Date(j.receivedAt).toLocaleString("en-CA", {
                    timeZone: "America/Halifax",
                  })}
                </Link>
              </td>
              <td>{j.bank}</td>
              <td>{j.purpose}</td>
              <td>
                {j.status}
                {j.error ? `: ${j.error}` : ""}
              </td>
            </tr>
          ))}
        </tbody>
      </table>
      {jobs.length === 0 && <p>No jobs yet.</p>}
    </>
  );
}
