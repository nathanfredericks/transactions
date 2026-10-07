import { backend } from "../../../utils/backend";
import { redirect, unstable_rethrow } from "next/navigation";
import Link from "next/link";
import { Alert, Card, CardBody, Form, FormCheck, Table } from "react-bootstrap";
import SubmitButton from "../../../components/SubmitButton";
import {
  atlanticDate,
  displayDate,
  displayLabel,
} from "../../../utils/display";
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
  if (!bank)
    return (
      <Alert variant="warning">
        A bank is required. <Link href="/jobs">Return to bank activity</Link>.
      </Alert>
    );
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
    } catch (error) {
      unstable_rethrow(error);
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
      <Link href={`/jobs/${jobId}?bank=${bank}`}>← Job details</Link>
      <header>
        <h1>Review starting balances and transactions</h1>
        <p className="text-body-secondary mb-0">
          Snapshot: {atlanticDate(preview.baseline.at)} Atlantic.
        </p>
      </header>
      {error && (
        <Alert variant="danger">
          Approval failed. Check every selection against current YNAB data and
          try again.
        </Alert>
      )}
      <Alert variant="info" className="mb-0">
        <p>
          For each bank transaction, choose its existing YNAB entry or confirm
          that it is missing. A pending entry can be linked for settlement: its
          amount, date and cleared status will be updated when imports resume,
          keeping your payee, category and memo.
        </p>
        <p className="mb-0">
          Approval itself makes no YNAB changes. Missing transactions will be
          imported when imports resume.
        </p>
      </Alert>
      <Alert variant="warning" className="mb-0">
        Keep all imports disabled during this review. The previous importers
        must remain paused.
      </Alert>
      <Form action={approve} className="d-flex flex-column gap-3">
        {preview.review.map((row, index) => (
          <Card key={row.key}>
            <CardBody as="fieldset">
              <legend className="h5">{row.record.description}</legend>
              <p className="fw-semibold">{money(row.record.amount)}</p>
              <p className="text-body-secondary">
                <span className="d-block">{displayDate(row.record.date)}</span>
                <span className="d-block">
                  Status: {displayLabel(row.record.status)}
                </span>
                <span className="d-block">
                  Account: {preview.ynabAccounts[row.record.accountId]?.name}
                </span>
              </p>
              <p id={`help-${index}`} className="small">
                Known imported entries and nearby amount matches are shown.
                Confirm that the payee describes the same transaction, and
                review any differences before choosing.
              </p>
              {row.candidates.map((tx) => (
                <FormCheck
                  className="mb-2"
                  key={tx.id}
                  id={`match-${index}-${tx.id}`}
                  type="radio"
                  name={`record-${index}`}
                  value={tx.id}
                  defaultChecked={preview.baseline.links[row.key] === tx.id}
                  required
                  aria-describedby={`help-${index}`}
                  label={
                    <>
                      <span className="d-block">
                        Already in YNAB: {tx.payee_name || "Unnamed payee"}
                      </span>
                      <span className="d-block">{displayDate(tx.date)}</span>
                      <span className="d-block">
                        {money(tx.amount)}, {displayLabel(tx.cleared)}
                      </span>
                    </>
                  }
                />
              ))}
              {(row.settlementCandidates || []).map((tx) => (
                <FormCheck
                  className="mb-2"
                  key={tx.id}
                  id={`settle-${index}-${tx.id}`}
                  type="radio"
                  name={`record-${index}`}
                  value={`settle:${tx.id}`}
                  required
                  aria-describedby={`help-${index}`}
                  label={
                    <>
                      <span className="d-block">
                        Settle existing pending entry:{" "}
                        {tx.payee_name || "Unnamed payee"}
                      </span>
                      <span className="d-block">{displayDate(tx.date)}</span>
                      <span className="d-block">
                        Current amount: {money(tx.amount)}. Bank amount:{" "}
                        {money(row.record.amount)}.
                      </span>
                      <span className="d-block">
                        Mark cleared when imports resume.
                      </span>
                    </>
                  }
                />
              ))}
              <FormCheck
                id={`new-${index}`}
                type="radio"
                name={`record-${index}`}
                value="new"
                required
                aria-describedby={`help-${index}`}
                label="Missing from YNAB: import when imports resume"
              />
            </CardBody>
          </Card>
        ))}
        {preview.review.length === 0 && (
          <Card>
            <Table responsive className="mb-0">
              <caption>Starting balances in Canadian dollars.</caption>
              <thead>
                <tr>
                  <th scope="col">Account</th>
                  <th scope="col">Bank</th>
                  <th scope="col">YNAB</th>
                  <th scope="col">Proposed adjustment</th>
                </tr>
              </thead>
              <tbody>
                {preview.snapshot.accounts.map((account) => {
                  const target = preview.ynabAccounts[account.id];
                  return (
                    <tr key={account.id}>
                      <th scope="row">{account.name}</th>
                      <td>{money(account.balance)}</td>
                      <td>{target ? money(target.balance) : "Excluded"}</td>
                      <td>
                        {target
                          ? money(account.balance - target.balance)
                          : "None"}
                      </td>
                    </tr>
                  );
                })}
              </tbody>
            </Table>
          </Card>
        )}
        <Card>
          <CardBody>
            <FormCheck
              className="mb-3"
              id="reviewed"
              type="checkbox"
              name="reviewed"
              value="yes"
              required
              label="I reviewed these choices against YNAB, and the previous importers are paused."
            />
            <SubmitButton>Approve starting point</SubmitButton>
          </CardBody>
        </Card>
      </Form>
    </>
  );
}
