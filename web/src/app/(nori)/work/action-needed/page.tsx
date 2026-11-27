import { Suspense } from "react";
import { WorkActionNeededScreen } from "@/components/assistant/WorkScreen";

export default function WorkActionNeededPage() {
  return (
    <Suspense>
      <WorkActionNeededScreen />
    </Suspense>
  );
}
