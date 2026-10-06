import { cache } from "react";
import { backend } from "./backend";
import type { Payee, CategoryGroup } from "../types";
const lookups = cache(() =>
  backend<{ payees: Payee[]; categories: CategoryGroup[] }>("lookups"),
);
export async function getPayees() {
  return (await lookups()).payees
    .filter(
      (p) =>
        ![
          "Manual Balance Adjustment",
          "Reconciliation Balance Adjustment",
          "Starting Balance",
        ].includes(p.name),
    )
    .sort((a, b) => a.name.localeCompare(b.name));
}
export async function getCategories() {
  return (await lookups()).categories;
}
