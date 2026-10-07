"use server";
import { redirect, unstable_rethrow } from "next/navigation";
import { saveOverride } from "@/app/utils/overrides";
import type { InitialValues } from "@/app/types";
export async function updateOverride(
  id: string,
  values: InitialValues,
  revision: string,
) {
  try {
    await saveOverride(values, id, revision);
  } catch (error) {
    unstable_rethrow(error);
    return { error: "Unable to save override. Check the rules and try again." };
  }
  redirect("/");
}
