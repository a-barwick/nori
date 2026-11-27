"use client";

import { useState } from "react";
import { Bookmark, Monitor, Moon, Play, RotateCcw } from "lucide-react";
import { Button } from "@/components/ui/primitives";
import {
  createComputerCheckpoint,
  listComputerCheckpoints,
  resetComputer,
  restoreComputerCheckpoint,
  sleepComputer,
  wakeComputer,
} from "@/lib/assistant/computer";
import { relative } from "@/lib/assistant/format";
import { useAction, useResource, useResourceInvalidation } from "@/lib/assistant/useResource";
import type { ComputerCheckpoint, ComputerStatus } from "@/lib/contracts/assistant";
import { ComputerDesktopDrawer } from "./ComputerDesktopDrawer";
import { Loading, Notice, Pill, Row, Rows, Screen, Section } from "./ui";

function formatBytes(bytes: number): string {
  if (!Number.isFinite(bytes) || bytes <= 0) return "0 MB";
  const gb = bytes / (1024 * 1024 * 1024);
  if (gb >= 1) return `${gb.toFixed(1)} GB`;
  const mb = bytes / (1024 * 1024);
  return `${Math.max(1, Math.round(mb))} MB`;
}

export function computerStatusPill(status: ComputerStatus["status"]): { tone: "ok" | "working" | "neutral" | "danger" | "warning"; label: string } {
  switch (status) {
    case "ready":
      return { tone: "ok", label: "Active" };
    case "sleeping":
      return { tone: "neutral", label: "Asleep" };
    case "provisioning":
    case "resetting":
      return { tone: "working", label: status === "provisioning" ? "Starting" : "Resetting" };
    case "deleting":
    case "deleted":
      return { tone: "neutral", label: "Removing" };
    case "error":
      return { tone: "danger", label: "Needs attention" };
    default:
      return { tone: "neutral", label: "Ready to start" };
  }
}

export function ComputerScreen() {
  const status = useResource<ComputerStatus>("/computer", { poll: 15_000, versioned: false });
  const invalidate = useResourceInvalidation();
  const action = useAction();
  const [drawerOpen, setDrawerOpen] = useState(false);
  const [confirmReset, setConfirmReset] = useState(false);
  const [loadedCheckpoints, setLoadedCheckpoints] = useState<{ computerID?: string; items: ComputerCheckpoint[] }>();


  const data = status.data;
  const checkpointsOnDemand = data?.provider === "sprites";
  const spriteCheckpoints = loadedCheckpoints?.computerID === data?.computer_id ? loadedCheckpoints?.items : undefined;
  const checkpoints = checkpointsOnDemand ? spriteCheckpoints : data?.checkpoints;
  const pill = data ? computerStatusPill(data.status) : { tone: "neutral" as const, label: "Loading" };
  const diskUsageKnown = data != null && data.disk_used_bytes >= 0;
  const usedPct =
    data && diskUsageKnown && data.disk_max_bytes > 0
      ? Math.min(100, Math.round((data.disk_used_bytes / data.disk_max_bytes) * 100))
      : 0;

  async function loadCheckpoints() {
    const items = await listComputerCheckpoints();
    setLoadedCheckpoints({ computerID: data?.computer_id, items });
  }

  async function runAndRefresh(label: string, fn: () => Promise<unknown>, announcement: string) {
    await action.run(
      label,
      async () => {
        await fn();
        invalidate("/computer");
        await status.reload();
      },
      announcement,
    );
  }

  return (
    <Screen title="Computer" back="/more" backLabel="More">
      {status.loading && !data ? (
        <Loading label="Loading computer status…" />
      ) : data && !data.enabled ? (
        <Notice tone="neutral">
          Your personal computer isn&apos;t enabled on this server yet.
        </Notice>
      ) : data ? (
        <div className="space-y-6">
          {action.error && <Notice tone="danger">{action.error}</Notice>}

          <div className="rounded-card border border-card-border bg-surface p-5">
            <div className="flex flex-wrap items-start justify-between gap-4">
              <div className="flex items-start gap-3">
                <span className="grid size-10 shrink-0 place-items-center rounded-[12px] bg-accent-subtle text-accent-text">
                  <Monitor size={20} strokeWidth={1.85} aria-hidden />
                </span>
                <div>
                  <div className="flex items-center gap-2">
                    <h2 className="text-card font-semibold text-ink">Personal Computer</h2>
                    <Pill tone={pill.tone}>{pill.label}</Pill>
                  </div>
                  <p className="mt-1 text-meta text-ink-2">
                    A private, persistent workspace where Nori runs tools, builds documents, and uses a browser. Open the desktop anytime to sign into sites so Nori can stay signed in.
                  </p>
                  {data.last_active_at && (
                    <p className="mt-1.5 text-caption text-ink-3">
                      Last active {relative(data.last_active_at)}
                    </p>
                  )}
                </div>
              </div>

              <div className="flex flex-wrap items-center gap-2">
                <Button
                  variant="primary"
                  disabled={!!action.pending}
                  onClick={() => setDrawerOpen(true)}
                >
                  <Monitor size={15} className="mr-1.5" aria-hidden />
                  Open Desktop
                </Button>
                {data.status === "ready" ? (
                  <Button
                    disabled={!!action.pending}
                    onClick={() => void runAndRefresh("sleep", () => sleepComputer(), "Computer is now asleep.")}
                  >
                    <Moon size={15} className="mr-1.5" aria-hidden />
                    Sleep
                  </Button>
                ) : (
                  <Button
                    disabled={!!action.pending}
                    onClick={() => void runAndRefresh("wake", () => wakeComputer(), "Computer is awake.")}
                  >
                    <Play size={15} className="mr-1.5" aria-hidden />
                    Wake
                  </Button>
                )}
              </div>
            </div>

            {data.disk_max_bytes > 0 && (
              <div className="mt-5 border-t border-hairline pt-4">
                <div className="flex items-center justify-between text-meta">
                  <span className="text-ink-2">{diskUsageKnown ? "Storage" : "Storage limit"}</span>
                  <span className="font-medium text-ink">
                    {diskUsageKnown && `${formatBytes(data.disk_used_bytes)} of `}
                    {formatBytes(data.disk_max_bytes)}
                  </span>
                </div>
                {diskUsageKnown && (
                  <div className="mt-2 h-1.5 overflow-hidden rounded-full bg-fill-2">
                    <div
                      className="h-full rounded-full bg-accent transition-all"
                      style={{ width: `${usedPct}%` }}
                    />
                  </div>
                )}
              </div>
            )}
          </div>

          <Section
            title="Checkpoints"
            actions={
              <div className="flex flex-wrap gap-2">
                {checkpointsOnDemand && (
                  <Button
                    size="sm"
                    disabled={!!action.pending || data.status === "not_provisioned"}
                    onClick={() => void action.run("list-checkpoints", loadCheckpoints)}
                  >
                    {checkpoints ? "Refresh checkpoints" : "Load checkpoints"}
                  </Button>
                )}
                <Button
                  size="sm"
                  disabled={!!action.pending || data.status === "not_provisioned"}
                  onClick={() =>
                    void runAndRefresh(
                      "checkpoint",
                      async () => {
                        const saved = await createComputerCheckpoint("Manual checkpoint");
                        if (checkpointsOnDemand) {
                          setLoadedCheckpoints({
                            computerID: data.computer_id,
                            items: [...(checkpoints ?? []), saved],
                          });
                        }
                      },
                      "Checkpoint saved.",
                    )
                  }
                >
                  <Bookmark size={14} className="mr-1.5" aria-hidden />
                  Save checkpoint
                </Button>
              </div>
            }
          >
            {checkpoints?.length === 0 ? (
              <Notice tone="neutral">
                No checkpoints saved yet. Start your computer to create an automatic baseline.
              </Notice>
            ) : checkpoints ? (
              <Rows label="Checkpoints">
                {checkpoints.map((ckpt) => (
                  <Row
                    key={ckpt.id}
                    title={ckpt.label || ckpt.id}
                    detail={relative(ckpt.created_at)}
                    trailing={
                      <Button
                        size="sm"
                        disabled={!!action.pending}
                        onClick={() =>
                          void runAndRefresh(
                            `restore-${ckpt.id}`,
                            async () => {
                              await restoreComputerCheckpoint(ckpt.id);
                              setLoadedCheckpoints(undefined);
                            },
                            "Checkpoint restored.",
                          )
                        }
                      >
                        Restore
                      </Button>
                    }
                  />
                ))}
              </Rows>
            ) : null}
          </Section>

          <Section title="Maintenance">
            <div className="rounded-card border border-card-border bg-surface p-4">
              <div className="flex flex-wrap items-center justify-between gap-3">
                <div>
                  <p className="text-row font-semibold text-ink">Reset Computer</p>
                  <p className="text-meta text-ink-2">
                    Restores your computer to its clean baseline and clears saved browser logins and workspace files.
                  </p>
                </div>
                {confirmReset ? (
                  <div className="flex items-center gap-2">
                    <Button
                      size="sm"
                      variant="danger"
                      disabled={!!action.pending}
                      onClick={() => {
                        setConfirmReset(false);
                        void runAndRefresh("reset", async () => {
                          await resetComputer();
                          setLoadedCheckpoints(undefined);
                        }, "Computer reset to baseline.");
                      }}
                    >
                      Confirm reset
                    </Button>
                    <Button size="sm" disabled={!!action.pending} onClick={() => setConfirmReset(false)}>
                      Cancel
                    </Button>
                  </div>
                ) : (
                  <Button
                    size="sm"
                    variant="danger"
                    disabled={!!action.pending || data.status === "not_provisioned"}
                    onClick={() => setConfirmReset(true)}
                  >
                    <RotateCcw size={14} className="mr-1.5" aria-hidden />
                    Reset Computer
                  </Button>
                )}
              </div>
            </div>
          </Section>

          <ComputerDesktopDrawer
            open={drawerOpen}
            onOpenChange={setDrawerOpen}
            initialMode="control"
          />
        </div>
      ) : null}
    </Screen>
  );
}
