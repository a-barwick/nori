import { headers } from "next/headers";
import { MoreScreen } from "@/components/assistant/MoreScreen";
import { resolveUser } from "@/lib/runtime/auth";

export default async function MorePage() {
  const user = await resolveUser(await headers());
  const userEmail = "identity" in user ? user.identity.email : undefined;
  return <MoreScreen userEmail={userEmail} />;
}
