import { Suspense } from "react";
import { WorkTasksScreen } from "@/components/assistant/WorkScreen";

export default function WorkTasksPage() {
  return (
    <Suspense>
      <WorkTasksScreen />
    </Suspense>
  );
}
