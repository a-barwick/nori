import { notFound } from "next/navigation";
import { SettingsScreen, type SettingsSection } from "@/components/assistant/SettingsScreen";

const SECTIONS = ["permissions", "preferences", "memory", "notifications", "history", "diagnostics"];

export default async function Page({ params }: { params: Promise<{ section: string }> }) {
  const { section } = await params;
  if (!SECTIONS.includes(section)) notFound();
  return <SettingsScreen section={section as SettingsSection} />;
}
