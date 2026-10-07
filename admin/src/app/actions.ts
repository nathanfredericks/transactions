"use server";
import { redirect, unstable_rethrow } from "next/navigation";
import { removeOverride } from "@/app/utils/overrides";
export async function deleteOverride(id: string, revision: string) {
  try {
    await removeOverride(id, revision);
  } catch (error) {
    unstable_rethrow(error);
    return { error: "Unable to delete override. Please try again." };
  }
  redirect("/");
}
