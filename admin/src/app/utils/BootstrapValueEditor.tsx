import * as React from "react";
import type { ValueEditorProps } from "react-querybuilder";
import { standardClassnames, ValueEditor } from "react-querybuilder";

export const BootstrapValueEditor = (
  props: ValueEditorProps,
): React.JSX.Element => {
  const placeHolderText = props.fieldData?.placeholder ?? "";

  switch (props.type) {
    case "text":
      return (
        <input
          className={`${standardClassnames.valueListItem} form-control w-50 ${props.field === "merchant" ? "text-uppercase font-monospace" : ""}`}
          disabled={props.disabled}
          onChange={(e) => props.handleOnChange(e.target.value)}
          placeholder={placeHolderText}
          type="text"
          value={props.value}
        />
      );
  }

  return <ValueEditor {...props} />;
};
