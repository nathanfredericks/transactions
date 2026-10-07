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
const dateOptions: Intl.DateTimeFormatOptions = {
  month: "long",
  day: "numeric",
  year: "numeric",
};
const atlanticDayFormatter = new Intl.DateTimeFormat("en-US", {
  ...dateOptions,
  timeZone: "America/Halifax",
});
const calendarDayFormatter = new Intl.DateTimeFormat("en-US", {
  ...dateOptions,
  timeZone: "UTC",
});
const atlanticTimeFormatter = new Intl.DateTimeFormat("en-US", {
  hour: "numeric",
  minute: "2-digit",
  hour12: true,
  timeZone: "America/Halifax",
});

function parseDate(value: string) {
  const date = new Date(value);
  return !Number.isFinite(date.getTime()) || value.startsWith("0001-")
    ? undefined
    : date;
}

export function displayDate(value: string) {
  const date = parseDate(value);
  if (!date) return "Date unavailable";
  // Transaction dates are calendar dates, so never shift them into the previous day.
  const formatter = /^\d{4}-\d{2}-\d{2}$/.test(value)
    ? calendarDayFormatter
    : atlanticDayFormatter;
  return formatter.format(date);
}

export function displayTime(value: string) {
  const date = parseDate(value);
  return date ? atlanticTimeFormatter.format(date) : "Time unavailable";
}

export const atlanticDate = (value: string) =>
  parseDate(value)
    ? `${displayDate(value)} at ${displayTime(value)}`
    : "Time unavailable";
