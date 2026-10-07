"use server";
import { redirect, unstable_rethrow } from "next/navigation";
import { saveOverride } from "@/app/utils/overrides";
import type { InitialValues } from "@/app/types";
export async function putOverride(values: InitialValues) {
  try {
    await saveOverride(values);
  } catch (error) {
    unstable_rethrow(error);
    return { error: "Unable to save override. Check the rules and try again." };
  }
  redirect("/");
}
