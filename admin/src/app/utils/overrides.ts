import { backend } from "./backend";
import { validateValues } from "./rules";
import type { InitialValues, Override } from "../types";
export async function listOverrides(): Promise<Override[]> {
  return (await backend<Override[] | null>("rules.list")) ?? [];
}
export async function getOverride(id: string): Promise<Override | undefined> {
  return (await backend<Override | null>("rules.get", { id })) ?? undefined;
}
export async function saveOverride(
  values: InitialValues,
  id?: string,
  revision?: string,
): Promise<string> {
  const query = validateValues(values);
  const result = await backend<{ id: string }>("rules.save", {
    ...values,
    query,
    id,
    revision,
  });
  return result.id;
}
export async function removeOverride(id: string, revision: string) {
  await backend("rules.delete", { id, revision });
}
