import { Suspense } from "react";
import { AppDetailScreen } from "@/components/assistant/AppsScreen";

export default async function AppPage({ params }: { params: Promise<{ id: string }> }) {
  return <Suspense><AppDetailScreen id={decodeURIComponent((await params).id)} /></Suspense>;
}
