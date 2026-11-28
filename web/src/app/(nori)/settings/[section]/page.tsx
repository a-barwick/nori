import { headers } from "next/headers";
import { notFound, redirect } from "next/navigation";
import { SettingsScreen, type SettingsSection } from "@/components/assistant/SettingsScreen";
import { resolveUser } from "@/lib/runtime/auth";

const SECTIONS = ["notifications", "permissions", "preferences", "api-costs", "appearance", "models", "reset", "account", "history", "diagnostics"];

export default async function Page({
  params,
  searchParams,
}: {
  params: Promise<{ section: string }>;
  searchParams: Promise<Record<string, string | string[] | undefined>>;
}) {
  const { section } = await params;
  if (section === "api-keys") redirect("/settings/models");
  if (section === "memory") {
    const sp = await searchParams;
    const query = new URLSearchParams();
    for (const [key, value] of Object.entries(sp)) {
      if (typeof value === "string") query.set(key, value);
      else if (Array.isArray(value) && value[0]) query.set(key, value[0]);
    }
    redirect(query.size ? `/memory?${query.toString()}` : "/memory");
  }
  if (!SECTIONS.includes(section)) notFound();
  let userEmail: string | undefined;
  if (section === "account") {
    const user = await resolveUser(await headers());
    if ("identity" in user) userEmail = user.identity.email;
  }
  return <SettingsScreen section={section as SettingsSection} userEmail={userEmail} />;
}
