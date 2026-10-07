import { Badge } from "react-bootstrap";
import { displayLabel } from "../utils/display";

export default function StatusBadge({ status }: { status: string }) {
  const variant = ["complete", "sent"].includes(status)
    ? "success"
    : ["failed", "review-required"].includes(status)
      ? "danger"
      : ["waiting", "paused"].includes(status)
        ? "warning"
        : "secondary";
  return (
    <Badge bg={variant} text={variant === "warning" ? "dark" : undefined}>
      {displayLabel(status)}
    </Badge>
  );
}
