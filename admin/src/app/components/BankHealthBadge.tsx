import { Badge } from "react-bootstrap";
import { displayLabel, type BankSummary } from "../utils/display";

export default function BankHealthBadge({ bank }: { bank: BankSummary }) {
  return (
    <Badge
      bg={
        bank.health.blocked || bank.health.importReview ? "danger" : "secondary"
      }
    >
      {bank.health.blocked
        ? "Sign-in paused"
        : bank.health.importReview
          ? "Imports need review"
          : !bank.enabled
            ? "Disabled"
            : bank.health.kind
              ? displayLabel(bank.health.kind)
              : "No current issues"}
    </Badge>
  );
}
