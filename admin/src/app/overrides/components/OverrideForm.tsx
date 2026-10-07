"use client";
import { useState } from "react";
import { serializeQuery } from "@/app/utils/rules";
import * as yup from "yup";
import { Formik, type FormikProps } from "formik";
import { Alert, Button, Card, Form } from "react-bootstrap";
import LinkButton from "@/app/components/LinkButton";
import NewTransactionForm from "@/app/overrides/components/NewTransactionForm";
import type { CategoryGroup, InitialValues, Payee } from "@/app/types";
import dynamic from "next/dynamic";

// The query builder assigns random element IDs, so render it only on the client.
const TransactionQueryBuilder = dynamic(
  () =>
    import("./TransactionQueryBuilder").then(
      (module) => module.TransactionQueryBuilder,
    ),
  {
    ssr: false,
    loading: () => (
      <p className="text-body-secondary" role="status">
        Loading matching rules…
      </p>
    ),
  },
);

type Props = {
  initialValues: InitialValues;
  payees: Payee[];
  categoryGroups: CategoryGroup[];
  onSubmit: (values: InitialValues) => Promise<void>;
};

export function OverrideForm(props: Props) {
  const { initialValues, payees, categoryGroups, onSubmit } = props;

  const [saveError, setSaveError] = useState("");
  const schema = yup.object().shape({
    name: yup.string().label("Rule name").required("Enter a rule name."),
    payee: yup.string().label("Payee").required("Choose a payee."),
    category: yup.string().nullable().label("Category"),
    memo: yup.string().nullable().label("Memo"),
    query: yup
      .object()
      .label("Query")
      .required()
      .test("valid-rules", "Check your matching rules.", (value) => {
        try {
          serializeQuery(value as InitialValues["query"]);
          return true;
        } catch {
          return false;
        }
      }),
  });

  return (
    <>
      <Formik
        initialValues={initialValues}
        onSubmit={async (values) => {
          setSaveError("");
          try {
            await onSubmit(values);
          } catch (error) {
            setSaveError(
              error instanceof Error
                ? error.message
                : "Unable to save override.",
            );
          }
        }}
        validateOnChange={false}
        validationSchema={schema}
      >
        {({
          isSubmitting,
          handleSubmit,
          values,
          setFieldValue,
          handleChange,
          errors,
        }: FormikProps<InitialValues>) => (
          <Form
            className="d-flex flex-column gap-4"
            noValidate
            onSubmit={handleSubmit}
          >
            {saveError && (
              <Alert variant="danger" role="alert">
                {saveError}
              </Alert>
            )}
            {errors.query && (
              <Alert variant="danger" role="alert">
                Check your matching rules.
              </Alert>
            )}
            <Form.Group className="mb-3" controlId="name">
              <Form.Label>Rule name</Form.Label>
              <Form.Control
                aria-describedby={errors.name ? "name-error" : undefined}
                isInvalid={!!errors.name}
                name="name"
                onChange={handleChange}
                type="text"
                value={values.name}
              />
              <Form.Control.Feedback id="name-error" type="invalid">
                {errors.name}
              </Form.Control.Feedback>
            </Form.Group>
            <Card>
              <Card.Header as="h2" className="h5">
                When a transaction matches
              </Card.Header>
              <Card.Body>
                <TransactionQueryBuilder
                  query={values.query}
                  setQuery={(query) => setFieldValue("query", query)}
                />
              </Card.Body>
            </Card>
            <Card>
              <Card.Header as="h2" className="h5">
                Apply these details
              </Card.Header>
              <Card.Body>
                <NewTransactionForm
                  categoryGroups={categoryGroups}
                  payees={payees}
                />
              </Card.Body>
            </Card>

            <div className="d-flex gap-2 flex-wrap justify-content-end">
              <LinkButton href="/" variant="outline-secondary">
                Cancel
              </LinkButton>
              <Button disabled={isSubmitting} type="submit" variant="primary">
                {isSubmitting ? "Saving…" : "Save rule"}
              </Button>
            </div>
          </Form>
        )}
      </Formik>
    </>
  );
}
