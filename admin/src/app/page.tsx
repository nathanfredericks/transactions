import { Button } from "react-bootstrap";
import Link from "next/link";
import OverridesList from "@/app/components/OverridesList";
import { listOverrides } from "@/app/utils/overrides";
export const dynamic = "force-dynamic";

export default async function Page() {
  const sortedOverrides = await listOverrides();

  return (
    <>
      <div className="d-flex justify-content-between align-items-center">
        <h1>Overrides</h1>
        <Link href="/overrides/new">
          <Button>New</Button>
        </Link>
      </div>

      <OverridesList overrides={sortedOverrides} />
    </>
  );
}
