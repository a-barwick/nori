import { MCPDetailScreen } from "@/components/assistant/AppsScreen";

export default async function MCPPage({ params }: { params: Promise<{ id: string }> }) {
  return <MCPDetailScreen id={decodeURIComponent((await params).id)} />;
}
