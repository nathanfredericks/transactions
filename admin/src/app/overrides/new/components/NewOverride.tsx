"use client";
import { CategoryGroup, InitialValues, Payee } from "@/app/types";
import { OverrideForm } from "@/app/overrides/components/OverrideForm";
import { putOverride } from "@/app/overrides/new/actions";

type Props = {
  payees: Payee[];
  categoryGroups: CategoryGroup[];
};

export default function NewOverride(props: Props) {
  const { payees, categoryGroups } = props;

  const initialValues: InitialValues = {
    name: "",
    payee: "",
    category: "",
    memo: "",
    query: {
      combinator: "and",
      rules: [{ field: "merchant", operator: "contains", value: "" }],
    },
  };

  return (
    <OverrideForm
      categoryGroups={categoryGroups}
      initialValues={initialValues}
      onSubmit={async (values) => {
        const result = await putOverride(values);
        if (result?.error) throw new Error(result.error);
      }}
      payees={payees}
    />
  );
}
