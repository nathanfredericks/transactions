"use client";
import { parseJsonLogic } from "react-querybuilder/parseJsonLogic";
import { useParams } from "next/navigation";
import { CategoryGroup, InitialValues, Payee } from "@/app/types";
import { OverrideForm } from "@/app/overrides/components/OverrideForm";
import { updateOverride } from "@/app/overrides/[override]/edit/actions";

type Props = {
  payees: Payee[];
  categoryGroups: CategoryGroup[];
  name: string;
  payee: string;
  category: string;
  memo: string;
  query: string;
  revision: string;
};

export default function EditOverride(props: Props) {
  const { payees, categoryGroups, name, payee, category, memo, query } = props;
  const { override: id } = useParams();

  const parsedJsonLogic = parseJsonLogic(query);
  const initialValues: InitialValues = {
    name,
    payee,
    category,
    memo,
    query: parsedJsonLogic,
  };

  return (
    <OverrideForm
      categoryGroups={categoryGroups}
      initialValues={initialValues}
      onSubmit={async (values) => {
        const result = await updateOverride(id as string, values, props.revision);
        if (result?.error) throw new Error(result.error);
      }}
      payees={payees}
    />
  );
}
