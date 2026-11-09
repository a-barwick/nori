import { PluginDetailScreen } from "@/components/assistant/AppsScreen";

export default async function PluginPage({ params }: { params: Promise<{ id: string }> }) {
  return <PluginDetailScreen id={decodeURIComponent((await params).id)} />;
}
