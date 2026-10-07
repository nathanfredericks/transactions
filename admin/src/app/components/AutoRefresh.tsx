"use client";

import { useEffect, useRef, useTransition } from "react";
import { usePathname, useRouter } from "next/navigation";

export default function AutoRefresh() {
  const router = useRouter();
  const pathname = usePathname();
  const [isPending, startTransition] = useTransition();
  const refreshing = useRef(false);

  useEffect(() => {
    refreshing.current = isPending;
  }, [isPending]);

  useEffect(() => {
    const timer = window.setInterval(() => {
      if (refreshing.current) return;
      refreshing.current = true;
      startTransition(() => router.refresh());
    }, 5_000);

    return () => window.clearInterval(timer);
  }, [pathname, router]);

  return null;
}
