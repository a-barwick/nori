import { WorkDetailScreen } from "@/components/assistant/WorkScreen";

export default async function WorkTaskPage({ params }: { params: Promise<{ id: string }> }) {
  return <WorkDetailScreen taskID={decodeURIComponent((await params).id)} />;
}
