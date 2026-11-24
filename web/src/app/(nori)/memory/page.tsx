import { Suspense } from "react";
import { MemoryScreen } from "@/components/assistant/MemoryScreen";

export default function Page() {
  return (
    <Suspense>
      <MemoryScreen />
    </Suspense>
  );
}
