import { headers } from "next/headers";
import { AppShell } from "@/components/assistant/AppShell";
import { runtimeMode } from "@/lib/runtime/target";

export default async function NoriLayout({ children }: LayoutProps<"/">) {
  return <AppShell localMode={runtimeMode(await headers()) === "local"}>{children}</AppShell>;
}
