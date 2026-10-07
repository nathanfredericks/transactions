"use client";
import {
  type ActionProps,
  defaultOperators,
  type Field,
  QueryBuilder,
  type RuleGroupType,
  toOptions,
  ValueEditor,
  type ValueEditorProps,
  type ValueSelectorProps,
  useSelectElementChangeHandler,
  useValueSelector,
} from "react-querybuilder";
import { Button, Form } from "react-bootstrap";
import "react-querybuilder/dist/query-builder-layout.css";
import { validNumber } from "@/app/utils/rules";
import { QueryBuilderBootstrap } from "@react-querybuilder/bootstrap";

type Props = {
  query: RuleGroupType;
  setQuery: (query: RuleGroupType) => void;
};

function RuleSelect(props: ValueSelectorProps) {
  const { onChange, val } = useValueSelector(props);
  const handleChange = useSelectElementChangeHandler({
    multiple: props.multiple,
    onChange,
  });
  return (
    <Form.Select
      className={props.className?.replace(
        /\bform-(?:select|control)(?:-sm|-lg)?\b/g,
        "",
      )}
      value={val}
      title={props.title}
      aria-label={props.title}
      disabled={props.disabled}
      multiple={!!props.multiple}
      onChange={handleChange}
      data-testid={props.testID}
    >
      {toOptions(props.options)}
    </Form.Select>
  );
}

function RuleValueEditor(props: ValueEditorProps) {
  return (
    <ValueEditor
      {...props}
      className={`${props.className?.replace(/\bform-(?:select|control)-(?:sm|lg)\b/g, "") ?? ""} form-control`}
      selectorComponent={RuleSelect}
    />
  );
}

function RuleAction(props: ActionProps & { variant?: string }) {
  return (
    <Button
      type="button"
      variant={props.variant ?? "primary"}
      title={props.title}
      disabled={props.disabled}
      onClick={(event) => props.handleOnClick(event)}
      data-testid={props.testID}
    >
      {props.label}
    </Button>
  );
}

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
          ruleGroup: "border rounded p-3",
          combinators: "w-auto",
        }}
        controlElements={{
          combinatorSelector: RuleSelect,
          fieldSelector: RuleSelect,
          operatorSelector: RuleSelect,
          valueSelector: RuleSelect,
          valueEditor: RuleValueEditor,
          addGroupAction: (props) =>
            props.level === 0 ? (
              <RuleAction {...props} label="Add group" variant="secondary" />
            ) : null,
          addRuleAction: (props) => <RuleAction {...props} label="Add rule" />,
          removeRuleAction: (props) => (
            <RuleAction {...props} label="Remove" variant="danger" />
          ),
          removeGroupAction: (props) => (
            <RuleAction {...props} label="Remove" variant="danger" />
          ),
        }}
        fields={fields}
        onQueryChange={setQuery}
        query={query}
      />
    </QueryBuilderBootstrap>
  );
}
