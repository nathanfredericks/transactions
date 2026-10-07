import "server-only";
import { InvokeCommand, LambdaClient } from "@aws-sdk/client-lambda";
import { requireAdmin } from "@/lib/require-admin";
const client = new LambdaClient({
  region: process.env.AWS_REGION || "ca-central-1",
});
export async function backend<T>(
  action: string,
  payload?: unknown,
  job?: { bank: string; jobId: string },
  submission?: unknown,
): Promise<T> {
  await requireAdmin();
  if (!process.env.BACKEND_FUNCTION)
    throw new Error("Backend is not configured.");
  const result = await client.send(
    new InvokeCommand({
      FunctionName: process.env.BACKEND_FUNCTION,
      Payload: Buffer.from(
        JSON.stringify({ action, payload, ...job, job: submission }),
      ),
    }),
  );
  if (result.FunctionError || !result.Payload)
    throw new Error("The operation failed. Refresh the page before retrying.");
  return JSON.parse(Buffer.from(result.Payload).toString()) as T;
}
