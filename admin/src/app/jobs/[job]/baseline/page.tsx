import { backend } from "../../../utils/backend";
import { redirect } from "next/navigation";
export const dynamic = "force-dynamic";
type Candidate = {
  id: string;
  date: string;
  payee_name: string | null;
  amount: number;
  cleared: string;
};
type Preview = {
  baseline: { links: Record<string, string>; at: string };
  review: {
    key: string;
    record: {
      accountId: string;
      description: string;
      amount: number;
      date: string;
      status: string;
    };
    candidates: Candidate[];
    settlementCandidates: Candidate[];
  }[];
  snapshot: { accounts: { id: string; name: string; balance: number }[] };
  ynabAccounts: Record<string, { name: string; balance: number }>;
};
const money = (amount: number) =>
  new Intl.NumberFormat("en-CA", { style: "currency", currency: "CAD" }).format(
    amount / 1000,
  );
export default async function Baseline({
  params,
  searchParams,
}: {
  params: Promise<{ job: string }>;
  searchParams: Promise<{ bank?: string; error?: string }>;
}) {
  const { job: jobId } = await params;
  const { bank, error } = await searchParams;
  if (!bank) return <p role="alert">A bank is required.</p>;
  const preview = await backend<Preview>("baseline.preview", undefined, {
    bank,
    jobId,
  });
  async function approve(form: FormData) {
    "use server";
    let failed = false;
    try {
      if (form.get("reviewed") !== "yes") throw new Error("Review required");
      const links: Record<string, string> = {};
      const settlements: Record<string, string> = {};
      const newRecords: string[] = [];
      for (const [index, row] of preview.review.entries()) {
        const decision = form.get(`record-${index}`);
        if (decision === "new") newRecords.push(row.key);
        else if (
          typeof decision === "string" &&
          decision.startsWith("settle:") &&
          row.settlementCandidates.some((tx) => tx.id === decision.slice(7))
        )
          settlements[row.key] = decision.slice(7);
        else if (
          typeof decision === "string" &&
          row.candidates.some((tx) => tx.id === decision)
        )
          links[row.key] = decision;
        else throw new Error("Each record needs a decision");
      }
      await backend(
        "baseline.approve",
        { approved: true, links, settlements, newRecords },
        { bank: bank!, jobId },
      );
    } catch {
      failed = true;
    }
    redirect(
      failed
        ? `/jobs/${jobId}/baseline?bank=${bank}&error=approval`
        : `/banks/${bank}`,
    );
  }
  return (
    <>
      <h1>Review starting balances and transactions</h1>
      {error && (
        <p role="alert">
          Approval failed. Check every selection against current YNAB data and
          try again.
        </p>
      )}
      <p>
        Snapshot:{" "}
        {new Date(preview.baseline.at).toLocaleString("en-CA", {
          timeZone: "America/Halifax",
        })}{" "}
        Atlantic.
      </p>
      <p>
        For each bank transaction, choose its existing YNAB entry or confirm
        that it is missing. A pending entry can be linked for settlement: its
        amount, date and cleared status will be updated after cutover while
        keeping your payee, category and memo. Missing transactions will be imported after cutover.
        Approval itself makes no YNAB changes.
      </p>
      <p>
        Pause the old importers and keep new imports disabled during this final
        review.
      </p>
      <form action={approve}>
        {preview.review.map((row, index) => (
          <fieldset className="border rounded p-3 mb-3" key={row.key}>
            <legend className="fs-5">
              {row.record.description} · {money(row.record.amount)}
            </legend>
            <p>
              {row.record.date} · {row.record.status} ·{" "}
              {preview.ynabAccounts[row.record.accountId]?.name}
            </p>
            <p id={`help-${index}`}>
              Known imported entries and nearby amount matches are shown.
              Confirm that the payee describes the same transaction, and review
              any differences before choosing.
            </p>
            {row.candidates.map((tx) => (
              <label className="d-block py-2" key={tx.id}>
                <input
                  type="radio"
                  name={`record-${index}`}
                  value={tx.id}
                  defaultChecked={preview.baseline.links[row.key] === tx.id}
                  required
                  aria-describedby={`help-${index}`}
                />{" "}
                Already in YNAB: {tx.date} · {tx.payee_name || "Unnamed payee"}{" "}
                · {money(tx.amount)} · {tx.cleared}
              </label>
            ))}
            {(row.settlementCandidates || []).map((tx) => (
              <label className="d-block py-2" key={`settle-${tx.id}`}>
                <input
                  type="radio"
                  name={`record-${index}`}
                  value={`settle:${tx.id}`}
                  required
                  aria-describedby={`help-${index}`}
                />{" "}
                Settle existing pending entry: {tx.date} · {tx.payee_name || "Unnamed payee"}
                {" "}· {money(tx.amount)} → {money(row.record.amount)} · mark cleared after cutover
              </label>
            ))}
            <label className="d-block py-2">
              <input
                type="radio"
                name={`record-${index}`}
                value="new"
                required
                aria-describedby={`help-${index}`}
              />{" "}
              Missing from YNAB — import after cutover
            </label>
          </fieldset>
        ))}
        {preview.review.length === 0 && (
          <ul>
            {preview.snapshot.accounts.map((account) => {
              const target = preview.ynabAccounts[account.id];
              return (
                <li key={account.id}>
                  {account.name}:{" "}
                  {target
                    ? `bank ${money(account.balance)}, YNAB ${money(target.balance)}, proposed adjustment ${money(account.balance - target.balance)}`
                    : "excluded from importing"}
                </li>
              );
            })}
          </ul>
        )}
        <label className="d-block py-2">
          <input type="checkbox" name="reviewed" value="yes" required /> I
          reviewed these choices against YNAB, and the old importers are paused.
        </label>
        <p>
          <button type="submit" className="btn btn-primary">
            Approve starting point
          </button>
        </p>
      </form>
    </>
  );
}
