import { SkillScreen } from "@/components/assistant/SkillsScreen";

export default async function Page({ params }: { params: Promise<{ id: string }> }) {
  return <SkillScreen id={decodeURIComponent((await params).id)} />;
}
