// DynamoDB
import type { RuleGroupType } from "react-querybuilder";

export type Override = {
  id: string;
  name: string;
  payee: string;
  category: string;
  memo: string;
  query: string;
  updatedAt: string;
  revision: string;
};

// YNAB
export type Payee = {
  id: string;
  name: string;
};

export type CategoryGroup = {
  id: string;
  name: string;
  categories: Category[];
};

type Category = {
  id: string;
  name: string;
};

// Formik
export type InitialValues = {
  name: string;
  payee: string;
  category: string;
  memo: string;
  query: RuleGroupType;
};
