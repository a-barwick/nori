import { mutate, newRequestID } from "@/lib/assistant/api";
import type { AuthAttempt, Connection } from "@/lib/contracts/assistant";
import "@/lib/desktop";

export function safeAuthorizeURL(url: string) {
  if (url.startsWith("/") && !url.startsWith("//") && !url.startsWith("/\\") && !url.includes("\\")) return url;
  try {
    return new URL(url).protocol === "https:" ? url : undefined;
  } catch {
    return undefined;
  }
}

export async function startSignIn(
  connection: Pick<Connection, "id" | "revision">,
  action: "connect" | "repair",
  returnTo: string,
  capabilities?: string[],
) {
  if (window.noriDesktop) {
    await window.noriDesktop.openInBrowser();
    return;
  }
  const receipt = (await mutate<Connection>(`/connections/${encodeURIComponent(connection.id)}/actions`, {
    request_id: newRequestID(),
    expected_revision: connection.revision,
    value: { action, return_to: returnTo, ...(capabilities?.length ? { capabilities } : {}) },
  })) as { auth?: AuthAttempt };
  const url = receipt.auth && safeAuthorizeURL(receipt.auth.authorize_url);
  if (!url) throw new Error("Nori didn’t return a sign-in link. Try again.");
  window.location.assign(url);
}
