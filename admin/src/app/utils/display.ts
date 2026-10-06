export type BankSummary = {
  bank: string;
  name: string;
  enabled: boolean;
  baselineApproved: boolean;
  health: { blocked: boolean; kind: string; importReview?: boolean };
};

const labels: Record<string, string> = {
  complete: "Completed",
  failed: "Failed",
  "review-required": "Needs review",
  paused: "Paused",
  waiting: "Waiting to retry",
  queued: "Queued",
  accepted: "Queued",
  browser: "Signing in",
  running: "In progress",
  "browser-required": "Signing in",
  "awaiting-browser": "Signing in",
  retrieve: "Bank retrieval",
  "maintain-session": "Session check",
  notification: "Purchase notification",
  sent: "Delivered",
  pending: "Pending",
  superseded: "No longer needed",
  "authentication-required": "Sign-in could not finish",
  "credentials-rejected": "Credentials rejected",
  "challenge-required": "Verification needed",
  "bank-maintenance": "Bank maintenance",
  throttled: "Bank request limit reached",
  temporary: "Temporary connection problem",
  "invalid-response": "Bank data needs review",
  "uncertain-write": "YNAB update needs review",
  "previous-import-missing": "Previously imported transaction is missing",
  "purchase-not-found": "Purchase not found",
};
export const displayLabel = (value: string) =>
  labels[value] ?? value.replaceAll("-", " ");
export const atlanticDate = (value: string) => {
  const date = new Date(value);
  return !Number.isFinite(date.getTime()) || value.startsWith("0001-")
    ? "Time unavailable"
    : date.toLocaleString("en-CA", { timeZone: "America/Halifax" });
};
