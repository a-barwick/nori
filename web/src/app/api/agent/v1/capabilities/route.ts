import { capabilitiesRoute } from "@/lib/runtime/forward";

export async function GET(request: Request) {
  return capabilitiesRoute(request, "local");
}
