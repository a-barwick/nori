import { uploadRoute } from "@/lib/runtime/forward";

export async function POST(request: Request) {
  return uploadRoute(request);
}
