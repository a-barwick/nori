import { headers } from "next/headers";
import { AppShell } from "@/components/assistant/AppShell";
import { runtimeMode } from "@/lib/runtime/target";

export default async function NoriLayout({ children }: LayoutProps<"/">) {
  const request = await headers();
  const scope = request.get("x-nori-resource-scope") ?? "anonymous";
  return <AppShell localMode={runtimeMode(request) === "local"} scope={scope}>{children}</AppShell>;
}
