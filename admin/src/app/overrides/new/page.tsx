export const dynamic = "force-dynamic";
import type { Metadata } from "next";
import { getCategories, getPayees } from "@/app/utils/ynab";
import NewOverride from "@/app/overrides/new/components/NewOverride";

export const metadata: Metadata = {
  title: "New rule | Transactions",
};

export default async function Page() {
  const payees = await getPayees();
  const categoryGroups = await getCategories();

  return (
    <>
      <div className="page-heading">
        <h1>New rule</h1>
      </div>
      <NewOverride categoryGroups={categoryGroups} payees={payees} />
    </>
  );
}
