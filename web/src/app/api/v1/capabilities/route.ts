import { capabilitiesRoute, conversationMode } from "@/lib/runtime/forward";

export async function GET(request: Request) {
  return capabilitiesRoute(request, conversationMode(request));
}
