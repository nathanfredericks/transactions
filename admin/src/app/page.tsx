import Link from "next/link";
import OverridesList from "@/app/components/OverridesList";
import { listOverrides } from "@/app/utils/overrides";
export const dynamic = "force-dynamic";

export default async function Page() {
  const sortedOverrides = await listOverrides();

  return (
    <>
      <header className="page-heading">
        <div>
          <h1>Transaction rules</h1>
          <p>Choose how matching purchases appear in YNAB.</p>
        </div>
        <Link className="btn btn-primary" href="/overrides/new">
          New rule
        </Link>
      </header>

      <OverridesList overrides={sortedOverrides} />
    </>
  );
}
