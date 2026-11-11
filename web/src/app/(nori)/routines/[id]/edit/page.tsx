import { RoutineEditorScreen } from "@/components/assistant/RoutinesScreen";

export default async function Page({ params }: { params: Promise<{ id: string }> }) {
  return <RoutineEditorScreen id={decodeURIComponent((await params).id)} />;
}
