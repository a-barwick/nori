import { HomePairingForm } from "@/components/assistant/HomeConnectorSettings";
import { Screen } from "@/components/assistant/ui";

export default async function ConnectorActivate({ searchParams }: { searchParams: Promise<{ user_code?: string }> }) {
  const { user_code: userCode } = await searchParams;
  return <Screen title="Pair a computer" back="/settings/models" backLabel="Models">
    <div className="max-w-md rounded-card border border-card-border bg-surface p-4">
      <HomePairingForm initialCode={userCode ?? ""} />
    </div>
  </Screen>;
}
