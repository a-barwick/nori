import { GoalEditorScreen } from "@/components/assistant/GoalsScreen";

export default async function Page({ params }: { params: Promise<{ id: string }> }) {
  return <GoalEditorScreen id={decodeURIComponent((await params).id)} />;
}
