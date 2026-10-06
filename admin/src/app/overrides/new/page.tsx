export const dynamic = "force-dynamic";
import type { Metadata } from "next";
import { getCategories, getPayees } from "@/app/utils/ynab";
import NewOverride from "@/app/overrides/new/components/NewOverride";

export const metadata: Metadata = {
  title: "New Override | Transactions",
};

export default async function Page() {
  const payees = await getPayees();
  const categoryGroups = await getCategories();

  return (
    <>
      <div className="d-flex justify-content-between align-items-center">
        <h1>New Override</h1>
      </div>
      <NewOverride categoryGroups={categoryGroups} payees={payees} />
    </>
  );
}
