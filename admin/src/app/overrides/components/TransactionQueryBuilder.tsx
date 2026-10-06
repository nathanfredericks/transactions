"use client";
import {
  ActionElement,
  defaultOperators,
  type Field,
  QueryBuilder,
  type RuleGroupType,
} from "react-querybuilder";
import "react-querybuilder/dist/query-builder-layout.css";
import { validNumber } from "@/app/utils/rules";
import { QueryBuilderBootstrap } from "@react-querybuilder/bootstrap";

type Props = {
  query: RuleGroupType;
  setQuery: (query: RuleGroupType) => void;
};

export function TransactionQueryBuilder(props: Props) {
  const { query, setQuery } = props;

  const months = [
    { value: "1", name: "1", label: "January" },
    { value: "2", name: "2", label: "February" },
    { value: "3", name: "3", label: "March" },
    { value: "4", name: "4", label: "April" },
    { value: "5", name: "5", label: "May" },
    { value: "6", name: "6", label: "June" },
    { value: "7", name: "7", label: "July" },
    { value: "8", name: "8", label: "August" },
    { value: "9", name: "9", label: "September" },
    { value: "10", name: "10", label: "October" },
    { value: "11", name: "11", label: "November" },
    { value: "12", name: "12", label: "December" },
  ];
  const date = new Date();
  const fields: Field[] = [
    {
      name: "merchant",
      label: "Merchant",
      operators: defaultOperators.filter((op) =>
        ["=", "!=", "contains", "doesNotContain"].includes(op.name),
      ),
      defaultOperator: "contains",
      className: "merchant",
    },
    {
      name: "amount",
      label: "Amount",
      inputType: "number",
      validator: (q) => validNumber(q.value, "amount"),
      operators: defaultOperators.filter((op) =>
        ["=", "!=", "<", ">", "<=", ">="].includes(op.name),
      ),
    },
    {
      name: "month",
      label: "Month",
      valueEditorType: "select",
      values: months,
      defaultValue: date.getMonth() + 1,
      operators: defaultOperators.filter((op) => ["=", "!="].includes(op.name)),
    },
    {
      name: "day",
      label: "Day",
      inputType: "number",
      defaultValue: date.getDate(),
      validator: (q) => validNumber(q.value, "day"),
      operators: defaultOperators.filter((op) =>
        ["=", "!=", "<", ">", "<=", ">="].includes(op.name),
      ),
    },
  ];

  return (
    <QueryBuilderBootstrap>
      <QueryBuilder
        controlClassnames={{
          ruleGroup: "p-3 card",
          combinators: "form-select w-auto",
          addRule: "btn btn-primary",
          addGroup: "btn btn-primary",
          fields: "form-select",
          operators: "form-select",
          removeRule: "btn btn-danger",
          removeGroup: "btn btn-danger",
        }}
        controlElements={{
          addGroupAction: (props) =>
            props.level === 0 ? (
              <ActionElement {...props} label="Add group" />
            ) : null,
          addRuleAction: (props) => (
            <ActionElement {...props} label="Add rule" />
          ),
          removeRuleAction: (props) => (
            <ActionElement {...props} label="Remove" />
          ),
          removeGroupAction: (props) => (
            <ActionElement {...props} label="Remove" />
          ),
        }}
        fields={fields}
        onQueryChange={setQuery}
        query={query}
      />
    </QueryBuilderBootstrap>
  );
}
