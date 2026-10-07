import { writeFileSync } from "node:fs";

const names = [
  "BACKEND_FUNCTION",
  "APP_BASE_URL",
  "AUTH0_DOMAIN",
  "AUTH0_CLIENT_ID",
  "AUTH0_CLIENT_SECRET",
  "AUTH0_SECRET",
  "AUTH0_ALLOWED_EMAILS",
];
for (const name of names) {
  if (!process.env[name]?.trim()) throw new Error(`${name} is not configured.`);
}
writeFileSync(
  ".env.production",
  names
    .map(
      (name) =>
        `${name}=${JSON.stringify(process.env[name]).replaceAll("$", "\\$")}`,
    )
    .join("\n") + "\n",
  { mode: 0o600 },
);
