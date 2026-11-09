import { LibraryDetailScreen } from "@/components/assistant/LibraryScreen";

export default async function Page({ params }: { params: Promise<{ id: string }> }) {
  return <LibraryDetailScreen id={decodeURIComponent((await params).id)} />;
}
