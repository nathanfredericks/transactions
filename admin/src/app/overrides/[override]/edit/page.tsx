export const dynamic = "force-dynamic";
import { getOverride } from "@/app/utils/overrides";
import type { Metadata } from "next";
import { notFound } from "next/navigation";
import { getCategories, getPayees } from "@/app/utils/ynab";
import { DeleteOverrideButton } from "@/app/overrides/[override]/edit/components/DeleteOverrideButton";
import EditOverride from "@/app/overrides/[override]/edit/components/EditOverride";

export const metadata: Metadata = {
  title: "Edit rule | Transactions",
};

export default async function Page({
  params,
}: {
  params: Promise<{ override: string }>;
}) {
  const { override } = await params;

  const Item = await getOverride(override);

  if (!Item) {
    return notFound();
  }

  const payees = await getPayees();
  const categoryGroups = await getCategories();

  return (
    <>
      <header className="d-flex justify-content-between align-items-center flex-wrap gap-3">
        <h1 className="mb-0">Edit {Item.name || "rule"}</h1>
        <DeleteOverrideButton id={override} revision={Item.revision} />
      </header>
      <EditOverride
        category={Item.category || ""}
        categoryGroups={categoryGroups}
        memo={Item.memo || ""}
        name={Item.name || ""}
        payee={Item.payee || ""}
        payees={payees}
        query={Item.query || ""}
        revision={Item.revision}
      />
    </>
  );
}
