import {
  formatQuery,
  type RuleGroupType,
  type RuleType,
} from "react-querybuilder";
import type { InitialValues } from "@/app/types";

const operators: Record<string, string[]> = {
  merchant: ["=", "!=", "contains", "doesNotContain"],
  amount: ["=", "!=", "<", ">", "<=", ">="],
  day: ["=", "!=", "<", ">", "<=", ">="],
  month: ["=", "!="],
};
export function validNumber(value: unknown, field: string): boolean {
  if (typeof value !== "number" && typeof value !== "string") return false;
  if (String(value).trim() === "") return false;
  const n = Number(value);
  return (
    Number.isFinite(n) &&
    (field === "amount"
      ? n > 0
      : Number.isInteger(n) && n >= 1 && n <= (field === "day" ? 31 : 12))
  );
}
export function normalizeQuery(query: RuleGroupType): RuleGroupType {
  if (
    !query ||
    !["and", "or"].includes(query.combinator) ||
    !Array.isArray(query.rules) ||
    !query.rules.length
  )
    throw new Error("Add at least one valid matching rule.");
  return {
    ...query,
    rules: query.rules.map((rule) => {
      if ("rules" in rule) return normalizeQuery(rule as RuleGroupType);
      const r = rule as RuleType;
      if (!operators[r.field]?.includes(r.operator))
        throw new Error("Unsupported rule field or operator.");
      if (r.field === "merchant") {
        if (typeof r.value !== "string" || !r.value.trim())
          throw new Error("Merchant cannot be empty.");
        return { ...r, value: r.value.trim().toUpperCase() };
      }
      if (!validNumber(r.value, r.field))
        throw new Error(`Enter a valid ${r.field}.`);
      return { ...r, value: Number(r.value) };
    }),
  };
}
export function serializeQuery(query: RuleGroupType): string {
  return JSON.stringify(formatQuery(normalizeQuery(query), "jsonlogic"));
}
export function validateValues(values: InitialValues): string {
  if (!values || typeof values.name !== "string" || !values.name.trim())
    throw new Error("Override name is required.");
  if (typeof values.payee !== "string" || !values.payee.trim())
    throw new Error("Payee is required.");
  if (typeof values.category !== "string" || typeof values.memo !== "string")
    throw new Error("Invalid category or memo.");
  if (values.memo.length > 500)
    throw new Error("Memo cannot exceed 500 characters.");
  return serializeQuery(values.query);
}
