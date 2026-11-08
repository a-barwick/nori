import { NoriApp } from "@/components/runtime/NoriApp";
import { runtimeMode } from "@/lib/runtime/target";
import { headers } from "next/headers";

export default async function ChatPage() {
  return <NoriApp agentMode={runtimeMode(await headers()) === "local"} />;
}
