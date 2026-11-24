import { GoalDetailScreen } from "@/components/assistant/GoalsScreen";

export default async function Page({ params }: { params: Promise<{ id: string }> }) {
  return <GoalDetailScreen id={decodeURIComponent((await params).id)} />;
}
