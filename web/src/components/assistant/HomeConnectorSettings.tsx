"use client";

import { useEffect, useState } from "react";
import { Dialog } from "radix-ui";
import { Server } from "lucide-react";
import { Button } from "@/components/ui/primitives";
import {
  APIError, approveHomePairing, errorText, lookupHomePairing, revokeHomeConnector, testHomeConnector,
  type HomeConnector, type HomeConnectors, type HomePairing,
} from "@/lib/assistant/api";
import { announce } from "@/lib/assistant/announce";
import { relative } from "@/lib/assistant/format";
import { useAssistant } from "@/lib/assistant/context";
import { useResource, useResourceInvalidation } from "@/lib/assistant/useResource";
import { useHydrated } from "@/lib/hooks";
import type { DesktopBridge, DesktopHomeStatus } from "@/lib/desktop";
import { DesktopHomeSharing } from "./DesktopHomeSharing";
import { Confirm, Failure, Field, inputClass, Notice, Pill, Section } from "./ui";

const statusLabels: Record<HomeConnector["status"], string> = {
  online: "Online",
  offline: "Offline",
  ollama_unavailable: "Ollama unavailable",
};

export function HomeConnectorSettings() {
  const connectors = useResource<HomeConnectors>("/settings/home-connectors", { poll: 10_000, versioned: false });
  const [pairingOpen, setPairingOpen] = useState(false);
  const [downloadOpen, setDownloadOpen] = useState(false);
  const hydrated = useHydrated();
  const desktop = hydrated ? window.noriDesktop : undefined;
  const [desktopStatus, setDesktopStatus] = useState<DesktopHomeStatus>();

  useEffect(() => {
    const bridge = desktop;
    if (!bridge) return;
    let active = true;
    const check = () => {
      void bridge.homeStatus().then((status) => { if (active) setDesktopStatus(status); }).catch(() => undefined);
    };
    check();
    const timer = window.setInterval(check, 10_000);
    return () => { active = false; window.clearInterval(timer); };
  }, [desktop]);

  if (connectors.error instanceof APIError && connectors.error.status === 404 && !desktop) return null;
  if (!connectors.data) {
    if (!connectors.error) return null;
    return <Section title="Home AI Server">
      <div className="space-y-3">
        {desktop && <DesktopHomeSettings desktop={desktop} status={desktopStatus} onStatus={setDesktopStatus} devices={[]} available={false} />}
        <Failure error={connectors.error} onRetry={() => void connectors.reload()} />
      </div>
    </Section>;
  }
  if (!connectors.data.enabled && !desktop) return null;

  return (
    <Section title="Home AI Server">
      <div className="space-y-3">
        {connectors.error && <Failure error={connectors.error} onRetry={() => void connectors.reload()} />}
        {desktop && <DesktopHomeSettings desktop={desktop} status={desktopStatus} onStatus={setDesktopStatus} devices={connectors.data.devices} available={connectors.data.enabled} />}
        {connectors.data.enabled && <>
        <div className="rounded-card border border-card-border bg-surface p-4">
          <div className="flex flex-wrap items-center gap-3">
            <span className="grid size-9 shrink-0 place-items-center rounded-[10px] bg-accent-subtle text-accent-text"><Server size={18} aria-hidden /></span>
            <span className="min-w-0 flex-1 text-row font-semibold">Connect a computer</span>
            <Button size="sm" variant="tonal" onClick={() => setDownloadOpen(true)}>Get connector</Button>
            <Button size="sm" onClick={() => setPairingOpen(true)}>Enter code</Button>
          </div>
        </div>
        {!desktopStatus?.paired && connectors.data.devices.map((device) => <HomeDevice key={device.id} device={device} />)}
        </>}
      </div>
      <Dialog.Root open={downloadOpen} onOpenChange={setDownloadOpen}>
        <Dialog.Portal>
          <Dialog.Overlay className="scrim fixed inset-0 z-50 bg-overlay" />
          <Dialog.Content className="anim-pop fixed top-1/2 left-1/2 z-50 w-[min(440px,calc(100vw-32px))] -translate-x-1/2 -translate-y-1/2 space-y-4 rounded-sheet border border-card-border bg-sheet p-6 shadow-pop">
            <Dialog.Title className="text-section font-semibold text-ink">Get Nori Connector</Dialog.Title>
            <Dialog.Description className="text-meta text-ink-2">Install it on the computer running Ollama, then enter the code shown there.</Dialog.Description>
            <DownloadLinks downloads={connectors.data.downloads} />
            <Dialog.Close asChild><Button size="sm">Close</Button></Dialog.Close>
          </Dialog.Content>
        </Dialog.Portal>
      </Dialog.Root>
      <Dialog.Root open={pairingOpen} onOpenChange={setPairingOpen}>
        <Dialog.Portal>
          <Dialog.Overlay className="scrim fixed inset-0 z-50 bg-overlay" />
          <Dialog.Content className="anim-pop fixed top-1/2 left-1/2 z-50 w-[min(440px,calc(100vw-32px))] -translate-x-1/2 -translate-y-1/2 space-y-4 rounded-sheet border border-card-border bg-sheet p-6 shadow-pop">
            <Dialog.Title className="text-section font-semibold text-ink">Pair a computer</Dialog.Title>
            <Dialog.Description className="text-meta text-ink-2">Enter the code shown by Nori Connector.</Dialog.Description>
            <HomePairingForm onComplete={() => setPairingOpen(false)} />
          </Dialog.Content>
        </Dialog.Portal>
      </Dialog.Root>
    </Section>
  );
}

function DesktopHomeSettings(props: {
  desktop: DesktopBridge;
  status?: DesktopHomeStatus;
  onStatus: (status: DesktopHomeStatus) => void;
  devices: HomeConnector[];
  available: boolean;
}) {
  const { scope } = useAssistant();
  return <DesktopHomeSharing {...props} scope={scope} />;
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
  if (!options.length) return <Notice tone="neutral">Connector downloads are not available yet.</Notice>;
  return <div className="flex flex-col items-start gap-2">
    {options.map(([label, url]) => <a key={label} href={url} className="text-row font-semibold text-accent-text underline underline-offset-2">{label}</a>)}
  </div>;
}

function HomeDevice({ device }: { device: HomeConnector }) {
  const invalidate = useResourceInvalidation();
  const [pending, setPending] = useState(false);
  const [message, setMessage] = useState("");
  const [error, setError] = useState("");
  const refresh = () => invalidate("/settings/home-connectors", "/settings/models", "/capabilities");
  const test = async () => {
    setPending(true); setMessage(""); setError("");
    try {
      await testHomeConnector(device.id);
      setMessage("Connection test passed.");
      refresh();
    } catch (reason) { setError(errorText(reason)); }
    finally { setPending(false); }
  };
  const revoke = async () => {
    setPending(true); setMessage(""); setError("");
    try {
      await revokeHomeConnector(device.id);
      announce(device.name + " disconnected");
      refresh();
    } catch (reason) { setError(errorText(reason)); }
    finally { setPending(false); }
  };
  return <div className="space-y-3 rounded-card border border-card-border bg-surface p-4">
    <div className="flex items-center gap-3">
      <span className="grid size-9 shrink-0 place-items-center rounded-[10px] bg-fill-1 text-ink-2"><Server size={18} aria-hidden /></span>
      <span className="min-w-0 flex-1">
        <span className="block text-row font-semibold">{device.name}</span>
        {device.last_seen_at && device.status !== "online" && <span className="text-meta text-ink-2">Last seen {relative(device.last_seen_at)}</span>}
      </span>
      <Pill tone={device.status === "online" ? "ok" : "danger"}>{statusLabels[device.status] ?? "Unavailable"}</Pill>
    </div>
    {device.models?.length > 0 && <p className="text-meta text-ink-2">{device.models.map((model) => model.name).join(" · ")}</p>}
    {error && <Notice tone="danger">{error}</Notice>}
    {message && <p className="text-meta text-ink-2" role="status">{message}</p>}
    <div className="flex flex-wrap gap-2">
      <Button size="sm" disabled={pending || device.status !== "online"} onClick={() => void test()}>Test connection</Button>
      <Confirm title={"Disconnect " + device.name + "?"} body="Choose another model for any route using this computer." confirm="Disconnect" danger onConfirm={() => void revoke()} trigger={<Button size="sm" variant="danger" disabled={pending}>Disconnect</Button>} />
    </div>
  </div>;
}

export function HomePairingForm({ initialCode = "", onComplete }: { initialCode?: string; onComplete?: () => void }) {
  const invalidate = useResourceInvalidation();
  const [code, setCode] = useState(initialCode);
  const [pairing, setPairing] = useState<HomePairing>();
  const [pending, setPending] = useState(false);
  const [error, setError] = useState("");
  const [completed, setCompleted] = useState(false);
  const lookup = async () => {
    setPending(true); setError("");
    try { setPairing(await lookupHomePairing(code.trim())); }
    catch (reason) { setError(errorText(reason)); }
    finally { setPending(false); }
  };
  const approve = async () => {
    if (!pairing) return;
    setPending(true); setError("");
    try {
      await approveHomePairing(pairing.user_code);
      invalidate("/settings/home-connectors", "/settings/models", "/capabilities");
      announce(pairing.name + " paired");
      if (onComplete) onComplete();
      else setCompleted(true);
    } catch (reason) { setError(errorText(reason)); }
    finally { setPending(false); }
  };
  if (completed) return <Notice tone="ok">Computer paired. Choose your models in More → Models.</Notice>;
  return <div className="space-y-3">
    {pairing ? <>
      <p className="text-body">Pair <strong>{pairing.name}</strong>?</p>
      <p className="text-meta text-ink-2">Confirm that <strong>{pairing.user_code}</strong> matches the code on your computer. Nori sends model requests to this computer when you select a home model.</p>
      <div className="flex flex-wrap justify-end gap-2">
        <Button size="sm" disabled={pending} onClick={() => setPairing(undefined)}>Back</Button>
        <Button size="sm" variant="primary" disabled={pending} onClick={() => void approve()}>{pending ? "Pairing…" : "Pair device"}</Button>
      </div>
    </> : <form className="space-y-3" onSubmit={(event) => { event.preventDefault(); void lookup(); }}>
      <Field label="Device code"><input className={inputClass} autoComplete="off" autoCapitalize="characters" spellCheck={false} value={code} onChange={(event) => setCode(event.target.value)} maxLength={32} required /></Field>
      <Button size="sm" type="submit" variant="primary" disabled={pending || !code.trim()}>{pending ? "Checking…" : "Continue"}</Button>
    </form>}
    {error && <Notice tone="danger">{error}</Notice>}
  </div>;
}
