"use client";

import { useState } from "react";
import { useSearchParams } from "next/navigation";
import { ChevronRight } from "lucide-react";
import { Button } from "@/components/ui/primitives";
import { APIError, mutate, newRequestID, startGoogleDrivePicker } from "@/lib/assistant/api";
import { useAction, useResource, useResourceInvalidation } from "@/lib/assistant/useResource";
import { useStripSearchParams } from "@/lib/browser";
import type { Connection, Integration, Resource } from "@/lib/contracts/assistant";
import { startSignIn, connectionState } from "./AppsScreen";
import { AppBrandIcon } from "./BrandIcons";
import { safeAuthorizeURL } from "./InteractionCard";
import { Confirm, Failure, Loading, Notice, Pill, Row, Rows, Section, Switch } from "./ui";

type List<T> = { items: T[] };

type Service = {
  id: "calendar" | "drive" | "gmail";
  title: string;
  capabilities: string[];
};

const services: Service[] = [
  { id: "calendar", title: "Google Calendar", capabilities: ["calendar.read", "calendar.write"] },
  { id: "drive", title: "Google Drive", capabilities: ["files.read", "files.write"] },
  { id: "gmail", title: "Gmail", capabilities: ["mail.send"] },
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
  const [pendingService, setPendingService] = useState<Service | null>(null);

  if (integration.loading || (connections.loading && !connections.data)) {
    return (
      <Section title="Google">
        <Loading />
      </Section>
    );
  }
  if (!integration.data) {
    if (!integration.error || (integration.error instanceof APIError && integration.error.status === 404)) return null;
    return (
      <Section title="Google">
        <Failure error={integration.error} onRetry={() => void integration.reload()} />
      </Section>
    );
  }
  if (connections.error && !connections.data) {
    return (
      <Section title="Google">
        <Failure error={connections.error} onRetry={() => void connections.reload()} />
      </Section>
    );
  }

  const connection = (connections.data?.items ?? [])
    .filter((item) => item.integration_id === "google" && item.status !== "disconnected")
    .sort((a, b) => Number(!!b.account?.provider_id) - Number(!!a.account?.provider_id))[0];
  const granted = new Set(connection?.capabilities ?? []);
  const isGranted = (service: Service) => service.capabilities.every((capability) => granted.has(capability));
  const enabled = !!connection?.account?.provider_id && connection.status !== "disconnecting";
  const canSignIn = integration.data.availability === "available";
  const needsRepair = enabled && (connection.status === "needs_attention" || connection.status === "disabled");
  const statusDisplay = connection ? connectionState(connection) : undefined;

  const connectService = (service: Service) =>
    action.run(`connect:${service.id}`, async () => {
      let target = connection;
      if (!target) {
        const receipt = await mutate<Connection>("/connections", {
          request_id: newRequestID(),
          expected_revision: 0,
          value: { integration_id: "google", title: "Google", selected_resource_ids: [] },
        });
        target = receipt.resource;
        invalidate("/connections");
      }
      const wanted = new Set([...(target.capabilities ?? []), ...service.capabilities]);
      await startSignIn(target, enabled ? "repair" : "connect", "/apps", [...wanted]);
    });

  const disconnect = () =>
    action.run("disconnect", async () => {
      if (!connection) return;
      await mutate<Connection>(`/connections/${encodeURIComponent(connection.id)}/actions`, {
        request_id: newRequestID(),
        expected_revision: connection.revision,
        value: { action: "disconnect" },
      });
      invalidate("/connections", "/integrations");
    });

  const driveActive = enabled && isGranted(services[1]);

  return (
    <Section
      title="Google"
      actions={
        enabled ? (
          <div className="flex items-center gap-3">
            {needsRepair && (
              <button
                type="button"
                disabled={!canSignIn || !!action.pending}
                onClick={() =>
                  void action.run("reconnect", () =>
                    startSignIn(connection, "repair", "/apps", connection.capabilities),
                  )
                }
                className="text-meta font-semibold text-accent-text hover:underline disabled:opacity-50"
              >
                Reconnect
              </button>
            )}
            <Confirm
              danger
              title="Disconnect Google?"
              body="Nori will no longer have access to this Google account."
              confirm="Disconnect"
              onConfirm={() => void disconnect()}
              trigger={
                <button
                  type="button"
                  disabled={!!action.pending}
                  className="text-meta font-semibold text-ink-3 hover:text-ink disabled:opacity-50"
                >
                  Disconnect
                </button>
              }
            />
          </div>
        ) : undefined
      }
    >
      {result === "drive_files_selected" && (
        <div className="mb-3">
          <Notice tone="ok">Your selected files are ready to use in Nori.</Notice>
        </div>
      )}
      {result === "cancelled" && (
        <div className="mb-3">
          <Notice tone="neutral">Google sign-in was cancelled.</Notice>
        </div>
      )}
      {result === "failed" && (
        <div className="mb-3">
          <Notice tone="danger">Google couldn’t finish the request. Try again.</Notice>
        </div>
      )}
      {enabled && statusDisplay && statusDisplay.tone !== "ok" && (
        <div className="mb-3 flex items-center gap-2">
          <Pill tone={statusDisplay.tone}>{statusDisplay.label}</Pill>
        </div>
      )}
      {!canSignIn && (
        <div className="mb-3">
          <Notice tone="neutral">
            {integration.data.availability_reason || "Google sign-in is currently unavailable."}
          </Notice>
        </div>
      )}
      {action.error && (
        <div className="mb-3">
          <Notice tone="danger">{action.error}</Notice>
        </div>
      )}

      <Rows label="Google services">
        {services.map((service) => {
          const active = enabled && isGranted(service);
          return (
            <li key={service.id}>
              <div className="flex min-h-14 w-full items-center gap-3 px-4 py-3">
                <AppBrandIcon service={service.id} />
                <span className="min-w-0 flex-1 truncate text-row font-semibold text-ink">{service.title}</span>
                <Switch
                  checked={active}
                  label={service.title}
                  disabled={active || !!action.pending || !canSignIn}
                  onCheckedChange={(next) => {
                    if (next && !active) {
                      setPendingService(service);
                    }
                  }}
                />
              </div>
              {service.id === "drive" && driveActive && <GoogleDrivePickerRow connection={connection} />}
            </li>
          );
        })}
      </Rows>

      {pendingService && (
        <div className="mt-3 space-y-3 rounded-card border border-card-border bg-surface p-4">
          <p className="text-row font-semibold text-ink">Connect {pendingService.title}?</p>
          <p className="text-meta text-ink-2">
            Nori uses the Google data you choose to carry out your requests and may share it with your selected AI model provider. Google will ask you to approve access.
          </p>
          <div className="flex flex-wrap gap-2">
            <Button
              size="sm"
              variant="primary"
              disabled={!!action.pending}
              onClick={() => {
                const svc = pendingService;
                setPendingService(null);
                void connectService(svc);
              }}
            >
              Continue
            </Button>
            <Button size="sm" variant="quiet" onClick={() => setPendingService(null)}>
              Cancel
            </Button>
          </div>
        </div>
      )}

      {driveActive && <GoogleDriveFileList connection={connection} />}
    </Section>
  );
}

function GoogleDrivePickerRow({ connection }: { connection: Connection }) {
  const action = useAction();
  const chooseFiles = () =>
    action.run("choose", async () => {
      const data = await startGoogleDrivePicker(connection.id, "/apps");
      const url = data.authorize_url && safeAuthorizeURL(data.authorize_url);
      if (!url) throw new Error("Nori didn’t return a Google Drive link. Try again.");
      window.location.assign(url);
    });

  return (
    <div className="border-t border-hairline">
      <button
        type="button"
        disabled={!!action.pending}
        onClick={() => void chooseFiles()}
        className="flex min-h-12 w-full items-center gap-3 px-4 py-2.5 pl-16 text-left transition-colors hover:bg-fill-1 focus-visible:bg-fill-1 disabled:opacity-50"
      >
        <span className="min-w-0 flex-1 text-row font-semibold text-accent-text">
          {action.pending ? "Opening Google Drive…" : "Choose Drive files"}
        </span>
        <ChevronRight size={15} className="shrink-0 text-ink-3" aria-hidden />
      </button>
      {action.error && (
        <div className="px-4 pb-3">
          <Notice tone="danger">{action.error}</Notice>
        </div>
      )}
    </div>
  );
}

function GoogleDriveFileList({ connection }: { connection: Connection }) {
  const resources = useResource<List<Resource>>(`/connections/${encodeURIComponent(connection.id)}/resources`, {
    poll: 0,
  });
  const files = resources.data?.items.filter((item) => item.kind === "drive_file") ?? [];
  if (resources.error) {
    return (
      <div className="mt-3">
        <Notice tone="danger">
          Couldn’t load Drive files.{" "}
          <button type="button" className="font-semibold underline" onClick={() => void resources.reload()}>
            Try again
          </button>
        </Notice>
      </div>
    );
  }
  if (!files.length) return null;
  return (
    <div className="mt-3">
      <Rows label="Selected Drive files">
        {files.map((file) => (
          <Row key={file.ref.provider_id} title={file.title} wrapTitle />
        ))}
      </Rows>
    </div>
  );
}
