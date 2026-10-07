import { Accordion, Form } from "react-bootstrap";
import { useFormikContext } from "formik";
import { CategoryGroup, InitialValues, Payee } from "@/app/types";

type Props = {
  payees: Payee[];
  categoryGroups: CategoryGroup[];
};

export default function NewTransactionForm(props: Props) {
  const { categoryGroups, payees } = props;
  const { values, handleChange, errors } = useFormikContext<InitialValues>();

  return (
    <div>
      <Form.Group className="mb-3" controlId="payee">
        <Form.Label>Payee</Form.Label>
        <Form.Select
          aria-label="Select a payee"
          aria-describedby={errors.payee ? "payee-error" : undefined}
          isInvalid={!!errors.payee}
          name="payee"
          onChange={handleChange}
          value={values.payee}
        >
          <option value="">Choose a payee</option>
          {payees?.map((payee) => (
            <option key={payee.id} value={payee.id}>
              {payee.name}
            </option>
          ))}
        </Form.Select>
        <Form.Control.Feedback id="payee-error" type="invalid">
          {errors.payee}
        </Form.Control.Feedback>
      </Form.Group>

      <Form.Group className="mb-3" controlId="category">
        <Form.Label>Category</Form.Label>
        <Form.Select
          aria-label="Select a category"
          isInvalid={!!errors.category}
          name="category"
          onChange={handleChange}
          value={values.category}
        >
          <option value="">Keep the existing category</option>
          {categoryGroups?.map((group) => (
            <optgroup key={group.id} label={group.name}>
              {group.categories.map((category) => (
                <option key={category.id} value={category.id}>
                  {category.name}
                </option>
              ))}
            </optgroup>
          ))}
        </Form.Select>
        <Form.Control.Feedback type="invalid">
          {errors.category}
        </Form.Control.Feedback>
      </Form.Group>

      <Accordion className="mb-3">
        <Accordion.Item eventKey="memo-templates">
          <Accordion.Header>Memo templates</Accordion.Header>
          <Accordion.Body>
            <p>
              Use Go templates. <code>{"{{.Date}}"}</code> is the transaction
              date (YYYY-MM-DD).
            </p>
            <p>
              Format dates with Go layouts:{" "}
              <code className="text-break">
                {'{{formatDate .Date "January 2006"}}'}
              </code>
              .
            </p>
            <p>
              Previous month:{" "}
              <code className="text-break">
                {'{{formatDate (subtractMonthFromDate .Date) "January 2006"}}'}
              </code>
              . Month-end dates are clamped to the last valid day.
            </p>
          </Accordion.Body>
        </Accordion.Item>
      </Accordion>
      <Form.Group className="mb-3" controlId="memo">
        <Form.Label>Memo</Form.Label>
        <Form.Control
          isInvalid={!!errors.memo}
          name="memo"
          onChange={handleChange}
          value={values.memo}
        />
      </Form.Group>
    </div>
  );
}
