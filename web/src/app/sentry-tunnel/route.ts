import { createSentryTunnelHandler } from "./handler";

const handlePost = createSentryTunnelHandler();

export async function POST(request: Request) {
  return handlePost(request);
}
