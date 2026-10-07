import AutoRefresh from "@/app/components/AutoRefresh";
import LinkButton from "@/app/components/LinkButton";
import OverridesList from "@/app/components/OverridesList";
import { listOverrides } from "@/app/utils/overrides";
export const dynamic = "force-dynamic";

export default async function Page() {
  const sortedOverrides = await listOverrides();

  return (
    <>
      <AutoRefresh />
      <header className="d-flex justify-content-between align-items-center flex-wrap gap-3">
        <div>
          <h1>Transaction rules</h1>
          <p className="text-body-secondary mb-0">
            Choose how matching purchases appear in YNAB.
          </p>
        </div>
        <LinkButton href="/overrides/new">New rule</LinkButton>
      </header>

      <OverridesList overrides={sortedOverrides} />
    </>
  );
}
