export const dynamic = "force-dynamic";
import { getOverride } from "@/app/utils/overrides";
import type { Metadata } from "next";
import { notFound } from "next/navigation";
import { getCategories, getPayees } from "@/app/utils/ynab";
import { DeleteOverrideButton } from "@/app/overrides/[override]/edit/components/DeleteOverrideButton";
import EditOverride from "@/app/overrides/[override]/edit/components/EditOverride";

export const metadata: Metadata = {
  title: "Edit Override | Transactions",
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
      <div className="d-flex justify-content-between align-items-center">
        <h1>Edit {Item.name || "Override"}</h1>
        <DeleteOverrideButton id={override} revision={Item.revision} />
      </div>
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
