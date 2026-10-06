"use server";
import { redirect } from "next/navigation";
import { removeOverride } from "@/app/utils/overrides";
export async function deleteOverride(id: string, revision: string) {
  try {
    await removeOverride(id, revision);
  } catch {
    return { error: "Unable to delete override. Please try again." };
  }
  redirect("/");
}
