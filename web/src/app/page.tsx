import { NoriApp } from "@/components/runtime/NoriApp";

export default function ChatPage() {
  if (process.env.NODE_ENV === "development" && process.env.NORI_LOCAL_AGENT_URL) {
    return <NoriApp agentMode />;
  }
  return <NoriApp />;
}
