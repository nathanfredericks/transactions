"use server";
import { redirect } from "next/navigation";
import { saveOverride } from "@/app/utils/overrides";
import type { InitialValues } from "@/app/types";
export async function putOverride(values: InitialValues) {
  try {
    await saveOverride(values);
  } catch {
    return { error: "Unable to save override. Check the rules and try again." };
  }
  redirect("/");
}
