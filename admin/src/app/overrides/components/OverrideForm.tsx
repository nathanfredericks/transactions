"use client";
import { useState } from "react";
import { serializeQuery } from "@/app/utils/rules";
import * as yup from "yup";
import { Formik, type FormikProps } from "formik";
import { Alert, Button, Card, Form } from "react-bootstrap";
import Link from "next/link";
import NewTransactionForm from "@/app/overrides/components/NewTransactionForm";
import type { CategoryGroup, InitialValues, Payee } from "@/app/types";
import { TransactionQueryBuilder } from "@/app/overrides/components/TransactionQueryBuilder";

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
    name: yup.string().label("Override").required(),
    payee: yup.string().label("Payee").required(),
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
          <Form noValidate onSubmit={handleSubmit}>
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
              <Form.Label>Override name</Form.Label>
              <Form.Control
                isInvalid={!!errors.name}
                name="name"
                onChange={handleChange}
                type="text"
                value={values.name}
              />
            </Form.Group>
            <Card>
              <Card.Header>Query Builder</Card.Header>
              <Card.Body className="p-0">
                <TransactionQueryBuilder
                  query={values.query}
                  setQuery={(query) => setFieldValue("query", query)}
                />
              </Card.Body>
            </Card>
            <Card>
              <Card.Header>New Transaction</Card.Header>
              <Card.Body>
                <NewTransactionForm
                  categoryGroups={categoryGroups}
                  payees={payees}
                />
              </Card.Body>
            </Card>

            <div className="d-inline-flex column-gap-2 justify-content-end">
              <Link href="/">
                <Button type="reset" variant="secondary">
                  Cancel
                </Button>
              </Link>
              <Button disabled={isSubmitting} type="submit" variant="primary">
                Save
              </Button>
            </div>
          </Form>
        )}
      </Formik>
    </>
  );
}
