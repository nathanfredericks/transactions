import { backend } from "../../../utils/backend";
import { redirect } from "next/navigation";
export const dynamic = "force-dynamic";
type Preview = {
  baseline: {
    seen: Record<string, boolean>;
    links: Record<string, string>;
    at: string;
  };
  unresolved: {
    accountId: string;
    description: string;
    amount: number;
    date: string;
  }[];
};
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
      await backend(
        "baseline.approve",
        { approved: true, links: preview.baseline.links },
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
      <h1>Review baseline</h1>
      {error && (
        <p role="alert">
          Approval failed. Review unresolved entries and try again.
        </p>
      )}
      <p>
        Snapshot: {preview.baseline.at}. This approval marks{" "}
        {Object.keys(preview.baseline.seen).length} posted records as already
        handled and links {Object.keys(preview.baseline.links).length} pending
        records to existing YNAB entries. It does not create transactions.
      </p>
      <p>
        Verify the bank snapshot against YNAB before approving. Keep old writers
        paused during final baseline review and cutover.
      </p>
      <pre>{JSON.stringify(preview, null, 2)}</pre>
      {preview.unresolved.length > 0 ? (
        <p role="alert">
          Pending records require explicit YNAB transaction links through the
          operator command before approval.
        </p>
      ) : (
        <form action={approve}>
          <label>
            <input type="checkbox" name="reviewed" value="yes" required /> I
            reviewed the existing YNAB data and approve this baseline.
          </label>
          <p>
            <button type="submit" className="btn btn-primary">
              Approve baseline
            </button>
          </p>
        </form>
      )}
    </>
  );
}
