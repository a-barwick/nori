import { RoutineDetailScreen } from "@/components/assistant/RoutinesScreen";

export default async function Page({ params }: { params: Promise<{ id: string }> }) {
  return <RoutineDetailScreen id={decodeURIComponent((await params).id)} />;
}
