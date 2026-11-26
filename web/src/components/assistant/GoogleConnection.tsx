"use client";

import { useState } from "react";
import { useSearchParams } from "next/navigation";
import { Button } from "@/components/ui/primitives";
import { APIError, mutate, newRequestID, startGoogleDrivePicker } from "@/lib/assistant/api";
import { useAction, useResource, useResourceInvalidation } from "@/lib/assistant/useResource";
import { useStripSearchParams } from "@/lib/browser";
import type { Connection, Integration, Resource } from "@/lib/contracts/assistant";
import { startSignIn, connectionState } from "./AppsScreen";
import { safeAuthorizeURL } from "./InteractionCard";
import { Confirm, Failure, Loading, Notice, Pill, Row, Rows, Section } from "./ui";

type List<T> = { items: T[] };

type Service = {
  id: string;
  title: string;
  description: string;
  capabilities: string[];
};

const services: Service[] = [
  { id: "calendar", title: "Google Calendar", description: "Read and change events on your calendars.", capabilities: ["calendar.read", "calendar.write"] },
  { id: "drive", title: "Google Drive", description: "Work with Docs, Sheets, and PDFs you choose.", capabilities: ["files.read", "files.write"] },
  { id: "gmail", title: "Gmail", description: "Send messages and attachments after your approval.", capabilities: ["mail.send"] },
];

const RETURN_PARAMS = ["result", "connection"] as const;

function useGoogleReturn() {
  const params = useSearchParams();
  const [result] = useState(() => (params.get("mcp") ? null : params.get("result")));
  useStripSearchParams(RETURN_PARAMS, !!result);
  return result;
}

export function GoogleConnection() {
  const integration = useResource<Integration>("/integrations/google", { poll: 0 });
  const connections = useResource<List<Connection>>("/connections", { poll: 30_000 });
  const invalidate = useResourceInvalidation();
  const action = useAction();
  const result = useGoogleReturn();
  const [chosen, setChosen] = useState<string[]>([]);

  if (integration.loading || connections.loading && !connections.data) return <Section title="Google"><Loading /></Section>;
  if (!integration.data) {
    if (!integration.error || integration.error instanceof APIError && integration.error.status === 404) return null;
    return <Section title="Google"><Failure error={integration.error} onRetry={() => void integration.reload()} /></Section>;
  }
  if (connections.error && !connections.data) return <Section title="Google"><Failure error={connections.error} onRetry={() => void connections.reload()} /></Section>;

  const connection = (connections.data?.items ?? [])
    .filter((item) => item.integration_id === "google" && item.status !== "disconnected")
    .sort((a, b) => Number(!!b.account?.provider_id) - Number(!!a.account?.provider_id))[0];
  const granted = new Set(connection?.capabilities ?? []);
  const isGranted = (service: Service) => service.capabilities.every((capability) => granted.has(capability));
  const pending = services.filter((service) => chosen.includes(service.id) && !isGranted(service));
  const enabled = !!connection?.account?.provider_id && connection.status !== "disconnecting";
  const canSignIn = integration.data.availability === "available";

  const connect = () => action.run("connect", async () => {
    let target = connection;
    if (!target) {
      const receipt = await mutate<Connection>("/connections", {
        request_id: newRequestID(), expected_revision: 0,
        value: { integration_id: "google", title: "Google", selected_resource_ids: [] },
      });
      target = receipt.resource;
      invalidate("/connections");
    }
    const wanted = new Set([...(target.capabilities ?? []), ...pending.flatMap((service) => service.capabilities)]);
    await startSignIn(target, enabled ? "repair" : "connect", "/apps", [...wanted]);
  });

  const disconnect = () => action.run("disconnect", async () => {
    if (!connection) return;
    await mutate<Connection>(`/connections/${encodeURIComponent(connection.id)}/actions`, {
      request_id: newRequestID(), expected_revision: connection.revision, value: { action: "disconnect" },
    });
    setChosen([]);
    invalidate("/connections", "/integrations");
  });

  return (
    <Section title="Google">
      {result === "drive_files_selected" && <Notice tone="ok">Your selected files are ready to use in Nori.</Notice>}
      {result === "cancelled" && <Notice tone="neutral">Google sign-in was cancelled.</Notice>}
      {result === "failed" && <Notice tone="danger">Google couldn’t finish the request. Try again.</Notice>}
      {connection?.account?.label && <div className="mb-3 flex flex-wrap items-center gap-2 text-meta text-ink-2"><span>{connection.account.label}</span><Pill tone={connectionState(connection).tone}>{connectionState(connection).label}</Pill></div>}
      <fieldset disabled={!!action.pending || !canSignIn} className="space-y-2">
        <legend className="sr-only">Google services</legend>
        {services.map((service) => {
          const active = isGranted(service);
          const checked = active || chosen.includes(service.id);
          return <label key={service.id} className="flex min-h-14 cursor-pointer items-start gap-3 rounded-tile border border-card-border bg-surface px-4 py-3">
            <input type="checkbox" className="mt-1 accent-[var(--accent)]" checked={checked} disabled={active || !!action.pending || !canSignIn}
              onChange={() => setChosen((current) => current.includes(service.id) ? current.filter((id) => id !== service.id) : [...current, service.id])} />
            <span className="min-w-0 flex-1"><span className="block text-row font-semibold text-ink">{service.title}</span><span className="block text-meta text-ink-2">{service.description}</span></span>
            {active && <span className="text-meta text-ink-2">Connected</span>}
          </label>;
        })}
      </fieldset>
      {!canSignIn && <p className="mt-3 text-meta text-ink-2">{integration.data.availability_reason || "Google sign-in is currently unavailable."}</p>}
      {(pending.length > 0 || !enabled) && <p className="mt-3 text-meta text-ink-2">Nori uses the Google data you choose to carry out your requests and may share it with your selected AI model provider. Google will ask you to approve access.</p>}
      {action.error && <div className="mt-3"><Notice tone="danger">{action.error}</Notice></div>}
      <div className="mt-3 flex flex-wrap gap-2">
        {(pending.length > 0 || !enabled) && <Button variant="primary" disabled={!canSignIn || !!action.pending || pending.length === 0} onClick={() => void connect()}>{action.pending === "connect" ? "Opening Google…" : enabled ? "Allow selected services" : "Connect Google"}</Button>}
        {enabled && (connection.status === "needs_attention" || connection.status === "disabled") && <Button disabled={!canSignIn || !!action.pending} onClick={() => void action.run("reconnect", () => startSignIn(connection, "repair", "/apps", connection.capabilities))}>Reconnect</Button>}
        {enabled && <Confirm trigger={<Button variant="quiet" disabled={!!action.pending}>Disconnect</Button>} title="Disconnect Google?" body="Nori will no longer have access to this Google account." confirm="Disconnect" onConfirm={() => void disconnect()} />}
      </div>
      {enabled && isGranted(services[1]) && <GoogleDriveFiles connection={connection} />}
    </Section>
  );
}

function GoogleDriveFiles({ connection }: { connection: Connection }) {
  const resources = useResource<List<Resource>>(`/connections/${encodeURIComponent(connection.id)}/resources`, { poll: 0 });
  const action = useAction();
  const files = resources.data?.items.filter((item) => item.kind === "drive_file") ?? [];
  const chooseFiles = () => action.run("choose", async () => {
    const data = await startGoogleDrivePicker(connection.id, "/apps");
    const url = data.authorize_url && safeAuthorizeURL(data.authorize_url);
    if (!url) throw new Error("Nori didn’t return a Google Drive link. Try again.");
    window.location.assign(url);
  });
  return <div className="mt-5">
    <div className="mb-2 flex items-center justify-between gap-3"><h3 className="text-meta font-bold text-ink-2">Drive files</h3><Button size="sm" disabled={!!action.pending} onClick={() => void chooseFiles()}>{action.pending ? "Opening Google Drive…" : "Choose files"}</Button></div>
    {resources.loading && !resources.data ? <Loading /> : files.length > 0 ? <Rows>{files.map((file) => <Row key={file.ref.provider_id} title={file.title} wrapTitle />)}</Rows> : <p className="text-meta text-ink-2">Choose files to use them in chat.</p>}
    {resources.error && <div className="mt-2"><Notice tone="danger">Couldn’t load Drive files. <button type="button" className="font-semibold underline" onClick={() => void resources.reload()}>Try again</button></Notice></div>}
    {action.error && <div className="mt-2"><Notice tone="danger">{action.error}</Notice></div>}
  </div>;
}
