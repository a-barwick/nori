import { NoriApp } from "@/components/runtime/NoriApp";
import { LocalAgentApp } from "@/components/runtime/LocalAgentApp";

export default function ChatPage() {
  if (process.env.NODE_ENV === "development" && process.env.NORI_LOCAL_AGENT_URL) {
    return <LocalAgentApp />;
  }
  return <NoriApp />;
}
