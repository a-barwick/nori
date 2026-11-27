"use client";

import { useEffect, useState } from "react";
import { Dialog } from "radix-ui";
import { Monitor } from "lucide-react";
import { Button } from "@/components/ui/primitives";
import {
  APIError,
  approveHomePairing,
  errorText,
  lookupHomePairing,
  revokeHomeConnector,
  testHomeConnector,
  type HomeConnector,
  type HomeConnectors,
  type HomePairing,
} from "@/lib/assistant/api";
import { announce } from "@/lib/assistant/announce";
import { useAssistantConfig } from "@/lib/assistant/context";
import { useResource, useResourceInvalidation } from "@/lib/assistant/useResource";
import { useHydrated } from "@/lib/hooks";
import type { DesktopBridge, DesktopHomeStatus } from "@/lib/desktop";
import { BrandIconWell } from "./BrandIcons";
import { DesktopHomeSharing, findDesktopDevice, readSavedDesktopDeviceID } from "./DesktopHomeSharing";
import { Confirm, Empty, Failure, Field, inputClass, Notice, Pill, Row, Rows, Section } from "./ui";

const statusLabels: Record<HomeConnector["status"], string> = {
  online: "Online",
  offline: "Offline",
  ollama_unavailable: "Ollama unavailable",
};

export function HomeConnectorSettings() {
  const connectors = useResource<HomeConnectors>("/settings/home-connectors", { poll: 10_000, versioned: false });
  const [pairingOpen, setPairingOpen] = useState(false);
  const [selectedDeviceId, setSelectedDeviceId] = useState<string | null>(null);
  const hydrated = useHydrated();
  const desktop = hydrated ? window.noriDesktop : undefined;
  const [desktopStatus, setDesktopStatus] = useState<DesktopHomeStatus>();

  useEffect(() => {
    const bridge = desktop;
    if (!bridge) return;
    let active = true;
    const check = () => {
      void bridge
        .homeStatus()
        .then((status) => {
          if (active) setDesktopStatus(status);
        })
        .catch(() => undefined);
    };
    check();
    const timer = window.setInterval(check, 10_000);
    return () => {
      active = false;
      window.clearInterval(timer);
    };
  }, [desktop]);

  if (connectors.error instanceof APIError && connectors.error.status === 404 && !desktop) return null;
  if (!connectors.data) {
    if (!connectors.error) return null;
    return (
      <Section title="Home computer">
        <div className="space-y-3">
          {desktop && (
            <DesktopHomeSettings
              desktop={desktop}
              status={desktopStatus}
              onStatus={setDesktopStatus}
              devices={[]}
              available={false}
            />
          )}
          <Failure error={connectors.error} onRetry={() => void connectors.reload()} />
        </div>
      </Section>
    );
  }
  if (!connectors.data.enabled && !desktop) return null;

  return (
    <Section
      title="Home computer"
      actions={
        connectors.data.enabled ? (
          <button
            type="button"
            onClick={() => setPairingOpen(true)}
            className="text-meta font-semibold text-accent-text hover:underline"
          >
            + Pair
          </button>
        ) : undefined
      }
    >
      <div className="space-y-3">
        {connectors.error && <Failure error={connectors.error} onRetry={() => void connectors.reload()} />}
        {desktop && (
          <DesktopHomeSettings
            desktop={desktop}
            status={desktopStatus}
            onStatus={setDesktopStatus}
            devices={connectors.data.devices}
            available={connectors.data.enabled}
          />
        )}
        {connectors.data.enabled && (
          desktop ? (
            <DesktopFilteredHomeDevices
              devices={connectors.data.devices}
              desktopStatus={desktopStatus}
              selectedDeviceId={selectedDeviceId}
              onSelect={setSelectedDeviceId}
            />
          ) : (
            <HomeDevicesList
              devices={connectors.data.devices}
              selectedDeviceId={selectedDeviceId}
              onSelect={setSelectedDeviceId}
            />
          )
        )}
      </div>

      <Dialog.Root open={pairingOpen} onOpenChange={setPairingOpen}>
        <Dialog.Portal>
          <Dialog.Overlay className="scrim fixed inset-0 z-50 bg-overlay" />
          <Dialog.Content className="anim-pop fixed top-1/2 left-1/2 z-50 w-[min(440px,calc(100vw-32px))] -translate-x-1/2 -translate-y-1/2 space-y-4 rounded-sheet border border-card-border bg-sheet p-6 shadow-pop">
            <Dialog.Title className="text-section font-semibold text-ink">Pair a computer</Dialog.Title>
            <Dialog.Description className="text-meta text-ink-2">
              Enter the code shown by Nori Connector.
            </Dialog.Description>
            <HomePairingForm onComplete={() => setPairingOpen(false)} onClose={() => setPairingOpen(false)} />
            <div className="border-t border-hairline pt-4">
              <p className="mb-2 text-caption font-semibold text-ink-3">Get Nori Connector</p>
              <DownloadLinks downloads={connectors.data.downloads} />
            </div>
          </Dialog.Content>
        </Dialog.Portal>
      </Dialog.Root>
    </Section>
  );
}

function DesktopFilteredHomeDevices({
  devices,
  desktopStatus,
  selectedDeviceId,
  onSelect,
}: {
  devices: HomeConnector[];
  desktopStatus?: DesktopHomeStatus;
  selectedDeviceId: string | null;
  onSelect: (id: string | null) => void;
}) {
  const { scope } = useAssistantConfig();
  const hydrated = useHydrated();
  const savedDeviceId = hydrated ? readSavedDesktopDeviceID(scope) : undefined;
  const desktopDevice = findDesktopDevice(devices, desktopStatus, savedDeviceId);
  const visibleDevices = desktopDevice ? devices.filter((device) => device.id !== desktopDevice.id) : devices;
  if (!visibleDevices.length) return null;
  return <HomeDevicesList devices={visibleDevices} selectedDeviceId={selectedDeviceId} onSelect={onSelect} />;
}

function DesktopHomeSettings(props: {
  desktop: DesktopBridge;
  status?: DesktopHomeStatus;
  onStatus: (status: DesktopHomeStatus) => void;
  devices: HomeConnector[];
  available: boolean;
}) {
  const { scope } = useAssistantConfig();
  return <DesktopHomeSharing {...props} scope={scope} />;
}

function HomeDevicesList({
  devices,
  selectedDeviceId,
  onSelect,
}: {
  devices: HomeConnector[];
  selectedDeviceId: string | null;
  onSelect: (id: string | null) => void;
}) {
  if (!devices.length) {
    return <Empty>No home computer paired.</Empty>;
  }
  return (
    <Rows label="Home computers">
      {devices.map((device) => {
        const expanded = selectedDeviceId === device.id;
        return (
          <li key={device.id}>
            <Row
              onClick={() => onSelect(expanded ? null : device.id)}
              leading={
                <BrandIconWell brand={false}>
                  <Monitor size={17} strokeWidth={1.85} aria-hidden />
                </BrandIconWell>
              }
              title={device.name}
              trailing={
                device.status !== "online" ? (
                  <Pill tone={device.status === "ollama_unavailable" ? "warning" : "danger"}>
                    {statusLabels[device.status] ?? "Unavailable"}
                  </Pill>
                ) : undefined
              }
              chevron
            />
            {expanded && <HomeDeviceDetail device={device} onDisconnected={() => onSelect(null)} />}
          </li>
        );
      })}
    </Rows>
  );
}

function HomeDeviceDetail({ device, onDisconnected }: { device: HomeConnector; onDisconnected: () => void }) {
  const invalidate = useResourceInvalidation();
  const [pending, setPending] = useState(false);
  const [message, setMessage] = useState("");
  const [error, setError] = useState("");
  const refresh = () => invalidate("/settings/home-connectors", "/settings/models", "/capabilities");

  const test = async () => {
    setPending(true);
    setMessage("");
    setError("");
    try {
      await testHomeConnector(device.id);
      setMessage("Connection test passed.");
      refresh();
    } catch (reason) {
      setError(errorText(reason));
    } finally {
      setPending(false);
    }
  };

  const revoke = async () => {
    setPending(true);
    setMessage("");
    setError("");
    try {
      await revokeHomeConnector(device.id);
      announce(device.name + " disconnected");
      refresh();
      onDisconnected();
    } catch (reason) {
      setError(errorText(reason));
    } finally {
      setPending(false);
    }
  };

  return (
    <div className="space-y-3 border-t border-hairline px-4 py-3.5">
      {device.models?.length > 0 && (
        <div className="space-y-1">
          <p className="text-caption font-semibold text-ink-3">Available models</p>
          <p className="text-meta text-ink-2">{device.models.map((model) => model.name).join(" · ")}</p>
        </div>
      )}
      {error && <Notice tone="danger">{error}</Notice>}
      {message && (
        <p className="text-meta text-ink-2" role="status">
          {message}
        </p>
      )}
      <div className="flex flex-wrap gap-2">
        <Button size="sm" variant="neutral" disabled={pending || device.status !== "online"} onClick={() => void test()}>
          Test connection
        </Button>
        <Confirm
          title={`Disconnect ${device.name}?`}
          body="Choose another model for Chat or Work before disconnecting."
          confirm="Disconnect"
          danger
          onConfirm={() => void revoke()}
          trigger={
            <Button size="sm" variant="danger" disabled={pending}>
              Disconnect
            </Button>
          }
        />
      </div>
    </div>
  );
}

function DownloadLinks({ downloads }: { downloads?: HomeConnectors["downloads"] }) {
  const options = [
    ["Download for Mac", downloads?.macos],
    ["Download for Linux · Intel / AMD", downloads?.linux_amd64],
    ["Download for Linux · ARM", downloads?.linux_arm64],
  ].filter((option): option is [string, string] => {
    if (!option[1]) return false;
    try {
      const url = new URL(option[1]);
      return url.protocol === "https:" && !url.username && !url.password;
    } catch {
      return false;
    }
  });
  if (!options.length) return <p className="text-meta text-ink-2">Connector downloads are not available yet.</p>;
  return (
    <div className="flex flex-col items-start gap-2">
      {options.map(([label, url]) => (
        <a key={label} href={url} className="text-meta font-semibold text-accent-text hover:underline">
          {label}
        </a>
      ))}
    </div>
  );
}

export function HomePairingForm({
  initialCode = "",
  onComplete,
  onClose,
}: {
  initialCode?: string;
  onComplete?: () => void;
  onClose?: () => void;
}) {
  const invalidate = useResourceInvalidation();
  const [code, setCode] = useState(initialCode);
  const [pairing, setPairing] = useState<HomePairing>();
  const [pending, setPending] = useState(false);
  const [error, setError] = useState("");
  const [completed, setCompleted] = useState(false);

  const lookup = async () => {
    setPending(true);
    setError("");
    try {
      setPairing(await lookupHomePairing(code.trim()));
    } catch (reason) {
      setError(errorText(reason));
    } finally {
      setPending(false);
    }
  };

  const approve = async () => {
    if (!pairing) return;
    setPending(true);
    setError("");
    try {
      await approveHomePairing(pairing.user_code);
      invalidate("/settings/home-connectors", "/settings/models", "/capabilities");
      announce(pairing.name + " paired");
      if (onComplete) onComplete();
      else setCompleted(true);
    } catch (reason) {
      setError(errorText(reason));
    } finally {
      setPending(false);
    }
  };

  if (completed) return <Notice tone="ok">Computer paired. Choose your models in More → Models.</Notice>;

  return (
    <div className="space-y-3">
      {pairing ? (
        <>
          <p className="text-body">
            Pair <strong>{pairing.name}</strong>?
          </p>
          <p className="text-meta text-ink-2">
            Confirm that <strong>{pairing.user_code}</strong> matches the code on your computer. Nori sends model requests to this computer when you select a home model.
          </p>
          <div className="flex flex-wrap justify-end gap-2">
            <Button size="sm" disabled={pending} onClick={() => setPairing(undefined)}>
              Back
            </Button>
            <Button size="sm" variant="primary" disabled={pending} onClick={() => void approve()}>
              {pending ? "Pairing…" : "Pair device"}
            </Button>
          </div>
        </>
      ) : (
        <form
          className="space-y-3"
          onSubmit={(event) => {
            event.preventDefault();
            void lookup();
          }}
        >
          <Field label="Device code">
            <input
              className={inputClass}
              autoComplete="off"
              autoCapitalize="characters"
              spellCheck={false}
              value={code}
              onChange={(event) => setCode(event.target.value)}
              maxLength={32}
              required
            />
          </Field>
          <div className="flex flex-wrap justify-end gap-2">
            {onClose && (
              <Button size="sm" variant="quiet" disabled={pending} onClick={onClose}>
                Close
              </Button>
            )}
            <Button size="sm" type="submit" variant="primary" disabled={pending || !code.trim()}>
              {pending ? "Checking…" : "Continue"}
            </Button>
          </div>
        </form>
      )}
      {error && <Notice tone="danger">{error}</Notice>}
    </div>
  );
}
