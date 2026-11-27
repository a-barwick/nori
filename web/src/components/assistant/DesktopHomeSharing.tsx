"use client";

import { useCallback, useEffect, useState } from "react";
import { Dialog } from "radix-ui";
import { Monitor } from "lucide-react";
import { Button } from "@/components/ui/primitives";
import {
  APIError, approveHomePairing, errorText, getJSON, saveModelSettings, testHomeConnector,
  type HomeConnector, type HomeConnectors, type ModelProfile, type ModelSettings,
} from "@/lib/assistant/api";
import { announce } from "@/lib/assistant/announce";
import { useResourceInvalidation } from "@/lib/assistant/useResource";
import type { DesktopBridge, DesktopHomeStatus } from "@/lib/desktop";
import { useHydrated } from "@/lib/hooks";
import { Confirm, Field, inputClass, Notice, Pill } from "./ui";

type Stage = "models" | "background" | "pair" | "routes";
type LocalModel = { name: string; capabilities?: string[] };

const MAX_BACKGROUND_POLLS = 40;
const MAX_ROUTE_POLLS = 20;

type DesktopHomeStatusWithID = DesktopHomeStatus & {
  device_id?: string;
  deviceId?: string;
};

export function desktopDeviceIDKey(scope: string): string {
  return `nori.desktop.home-device-id:${scope}`;
}

export function readSavedDesktopDeviceID(scope: string): string | undefined {
  try {
    return localStorage.getItem(desktopDeviceIDKey(scope)) || undefined;
  } catch {
    return undefined;
  }
}

export function findDesktopDevice(
  devices: HomeConnector[],
  status?: DesktopHomeStatus,
  savedDeviceId?: string,
): HomeConnector | undefined {
  if (!status?.paired || !devices.length) return undefined;
  const extended = status as DesktopHomeStatusWithID;
  const statusId = extended.device_id ?? extended.deviceId;
  if (statusId) {
    return devices.find((device) => device.id === statusId);
  }
  if (savedDeviceId) {
    const matched = devices.find((device) => device.id === savedDeviceId);
    if (matched) return matched;
  }
  return devices.length === 1 ? devices[0] : undefined;
}

function readSavedSetupModel(key: string): string {
  try {
    return localStorage.getItem(key) ?? "";
  } catch {
    return "";
  }
}

export function DesktopHomeSharing({ desktop, status, onStatus, devices, available, scope }: {
  desktop: DesktopBridge;
  status?: DesktopHomeStatus;
  onStatus: (status: DesktopHomeStatus) => void;
  devices: HomeConnector[];
  available: boolean;
  scope: string;
}) {
  const hydrated = useHydrated();
  const invalidate = useResourceInvalidation();
  const setupKey = `nori.desktop.home-setup:${scope}`;
  const deviceKey = desktopDeviceIDKey(scope);
  const [stage, setStage] = useState<Stage>();
  const [models, setModels] = useState<LocalModel[]>([]);
  const [model, setModel] = useState("");
  const [pendingModelOverride, setPendingModelOverride] = useState<string>();
  const [savedDeviceIdOverride, setSavedDeviceIdOverride] = useState<string>();
  const pendingModel = pendingModelOverride ?? (hydrated ? readSavedSetupModel(setupKey) : "");
  const savedDeviceId = savedDeviceIdOverride ?? (hydrated ? readSavedDesktopDeviceID(scope) : undefined);

  const rememberDeviceId = useCallback((id: string) => {
    setSavedDeviceIdOverride(id || undefined);
    try {
      if (id) localStorage.setItem(deviceKey, id);
      else localStorage.removeItem(deviceKey);
    } catch { /* Setup can still continue in this window. */ }
  }, [deviceKey]);
  const [pending, setPending] = useState(false);
  const [error, setError] = useState("");
  const [message, setMessage] = useState("");
  const [routeData, setRouteData] = useState<{ model: string; profile?: ModelProfile }>();
  const [chat, setChat] = useState(false);
  const [work, setWork] = useState(false);
  const [routeAttempt, setRouteAttempt] = useState(0);

  const rememberModel = (name: string) => {
    setPendingModelOverride(name);
    try {
      if (name) localStorage.setItem(setupKey, name);
      else localStorage.removeItem(setupKey);
    } catch { /* Setup can still continue in this window. */ }
  };

  const refresh = useCallback(async () => {
    const current = await desktop.homeStatus();
    onStatus(current);
    invalidate("/settings/home-connectors", "/settings/models", "/capabilities");
    return current;
  }, [desktop, invalidate, onStatus]);

  useEffect(() => {
    if (stage !== "background") return;
    let active = true;
    let timer: ReturnType<typeof setTimeout> | undefined;
    let attempts = 0;
    const check = async () => {
      attempts += 1;
      try {
        const current = await desktop.homeStatus();
        if (!active) return;
        onStatus(current);
        if (current.background === "enabled") {
          invalidate("/settings/home-connectors", "/settings/models", "/capabilities");
          setStage(current.paired ? (pendingModel ? "routes" : undefined) : "pair");
          return;
        }
      } catch { /* The user can retry from the dialog. */ }
      if (active && attempts < MAX_BACKGROUND_POLLS) {
        timer = setTimeout(() => void check(), 3000);
      }
    };
    void check();
    return () => {
      active = false;
      if (timer !== undefined) clearTimeout(timer);
    };
  }, [desktop, invalidate, onStatus, pendingModel, stage]);

  useEffect(() => {
    if (stage !== "routes") return;
    const controller = new AbortController();
    let timer: ReturnType<typeof setTimeout> | undefined;
    let attempts = 0;
    const load = async () => {
      attempts += 1;
      try {
        const [settings, connectors] = await Promise.all([
          getJSON<ModelSettings>("/settings/models", controller.signal),
          getJSON<HomeConnectors>("/settings/home-connectors", controller.signal),
        ]);
        if (controller.signal.aborted) return;
        const device = findDesktopDevice(connectors.devices, status, savedDeviceId);
        const profile = settings.model_profiles?.profiles.find((item) =>
          item.backend === "home" && (!device || item.home_device_id === device.id) && item.model === model,
        );
        setRouteData({ model, profile });
        if (!profile && attempts < MAX_ROUTE_POLLS) {
          timer = setTimeout(() => void load(), 3000);
        }
      } catch (reason) {
        if (!controller.signal.aborted) setError(errorText(reason));
      }
    };
    void load();
    return () => {
      controller.abort();
      if (timer !== undefined) clearTimeout(timer);
    };
  }, [model, routeAttempt, savedDeviceId, stage, status]);

  const openSetup = async () => {
    setError(""); setMessage("");
    if (pendingModel) {
      setModel(pendingModel);
      setStage(status?.background === "enabled" ? (status.paired ? "routes" : "pair") : "background");
      return;
    }
    setStage("models");
    setPending(true);
    try {
      const inventory = await desktop.ollamaModels();
      if (!inventory.ollama_available) {
        setError("Start Ollama on this Mac, then check again.");
        setModels([]);
        return;
      }
      const available = (inventory.models ?? []).filter((item) => item.capabilities?.includes("completion"));
      setModels(available);
      setModel(available[0]?.name ?? "");
      if (!available.length) setError("Install a model that supports text generation in Ollama, then check again.");
    } catch {
      setError("Couldn’t check Ollama. Start it on this Mac, then try again.");
    } finally { setPending(false); }
  };

  const continueToBackground = () => {
    rememberModel(model);
    setStage(status?.background === "enabled" ? "pair" : "background");
  };

  const pair = async () => {
    if (!model || pending) return;
    setPending(true); setError("");
    let paired = false;
    const priorIds = new Set(devices.map((item) => item.id));
    try {
      const pairing = await desktop.beginPairing();
      await approveHomePairing(pairing.user_code);
      await desktop.finishPairing();
      paired = true;
      const latestConnectors = await getJSON<HomeConnectors>("/settings/home-connectors").catch(() => undefined);
      const added = latestConnectors?.devices.find((item) => !priorIds.has(item.id));
      if (added) rememberDeviceId(added.id);
      setStage("routes");
      void refresh().catch(() => undefined);
      invalidate("/settings/home-connectors", "/settings/models", "/capabilities");
    } catch (reason) {
      if (!paired) {
        await desktop.cancelPairing().catch(() => undefined);
        setStage("background");
      }
      setError(errorText(reason));
      await refresh().catch(() => undefined);
    } finally { setPending(false); }
  };

  const enableBackground = async () => {
    setPending(true); setError("");
    try {
      await desktop.registerBackground();
      const current = await desktop.homeStatus();
      onStatus(current);
      if (current.background === "enabled") setStage(current.paired ? (pendingModel ? "routes" : undefined) : "pair");
      else if (current.background === "approval_required") await desktop.openBackgroundSettings();
      else setError("Nori couldn’t enable background access. Try again.");
    } catch { setError("Nori couldn’t enable background access. Try again."); }
    finally { setPending(false); }
  };

  const saveRoutes = async () => {
    if (pending) return;
    if (!chat && !work) {
      rememberModel("");
      setStage(undefined);
      return;
    }
    setPending(true); setError("");
    try {
      const [settings, connectors] = await Promise.all([
        getJSON<ModelSettings>("/settings/models"),
        getJSON<HomeConnectors>("/settings/home-connectors"),
      ]);
      const device = findDesktopDevice(connectors.devices, status, savedDeviceId);
      const profile = settings.model_profiles?.profiles.find((item) =>
        item.backend === "home" && (!device || item.home_device_id === device.id) && item.model === model,
      );
      if (!profile || (chat && !profile.lanes.includes("chat")) || (work && !profile.lanes.includes("task"))) {
        throw new Error("The selected model is still connecting. Try again shortly.");
      }
      const selection = { ...settings.selection };
      if (chat) {
        selection.chat_profile_id = profile.id;
        selection.chat_reasoning = profile.default_reasoning;
      }
      if (work) {
        selection.task_profile_id = profile.id;
        selection.task_reasoning = profile.default_reasoning;
      }
      await saveModelSettings(selection, settings.revision);
      invalidate("/settings/models", "/capabilities");
      announce("Model settings saved");
      rememberModel("");
      setStage(undefined);
    } catch (reason) {
      setError(reason instanceof APIError && reason.stale
        ? "Model settings changed on another device. Review your choices and try again."
        : errorText(reason));
      setRouteAttempt((current) => current + 1);
    } finally { setPending(false); }
  };

  const disconnect = async () => {
    setPending(true); setError(""); setMessage("");
    try {
      const result = await desktop.disconnectHome();
      rememberModel("");
      rememberDeviceId("");
      await refresh();
      setMessage(result.revocationPending
        ? "Sharing is off. Nori will finish disconnecting when the network is available."
        : "Sharing is off.");
      announce("Home AI sharing turned off");
    } catch {
      setError("Nori couldn’t turn off sharing. Try again.");
    } finally { setPending(false); }
  };

  const device = findDesktopDevice(devices, status, savedDeviceId);
  const availability = status?.revocationPending ? "Disconnecting"
    : status?.background === "approval_required" ? "Background approval needed"
    : status?.background !== "enabled" ? "Off"
      : status.state === "online" && status.ollama_available === false ? "Ollama unavailable"
        : status.state === "online" && device?.status === "online" ? "Available"
          : status.state === "connecting" ? "Connecting"
          : "Offline";
  const availabilityTone = availability === "Offline"
    ? "danger" as const
    : availability === "Ollama unavailable" || availability === "Background approval needed"
      ? "warning" as const
      : availability === "Connecting"
        ? "working" as const
        : "neutral" as const;
  const profile = routeData?.model === model ? routeData.profile : undefined;
  const canChat = !!profile?.lanes.includes("chat");
  const canWork = !!profile?.lanes.includes("task");
  const testConnection = async () => {
    if (!device || pending) return;
    setPending(true); setError(""); setMessage("");
    try {
      await testHomeConnector(device.id);
      setMessage("Connection test passed.");
      invalidate("/settings/home-connectors", "/settings/models", "/capabilities");
    } catch (reason) { setError(errorText(reason)); }
    finally { setPending(false); }
  };

  return <>
    <div className="space-y-3 rounded-card border border-card-border bg-surface p-4">
      <div className="flex flex-wrap items-center gap-3">
        <span className="grid size-9 shrink-0 place-items-center rounded-[10px] bg-accent-subtle text-accent-text"><Monitor size={18} aria-hidden /></span>
        <span className="min-w-0 flex-1 text-row font-semibold">Use this Mac as a Home AI Server</span>
        {status?.paired && availability !== "Available" && <Pill tone={availabilityTone}>{availability}</Pill>}
      </div>
      {(status?.paired || (pendingModel && status && status.background !== "off")) && !status?.revocationPending ? <div className="flex flex-wrap gap-2">
        {pendingModel && <Button size="sm" onClick={() => void openSetup()}>Continue setup</Button>}
        {!pendingModel && status?.background !== "enabled" && <Button size="sm" onClick={() => { setStage("background"); }}>Enable background access</Button>}
        {device && <Button size="sm" disabled={pending || availability !== "Available"} onClick={() => void testConnection()}>Test connection</Button>}
        <Confirm title="Turn off sharing on this Mac?" body="Choose another model for Chat or Work before turning off sharing." confirm="Turn off sharing" danger
          onConfirm={() => void disconnect()} trigger={<Button size="sm" variant="danger" disabled={pending}>Turn off sharing</Button>} />
      </div> : <Button size="sm" variant="primary" disabled={pending || !status || !available || status.revocationPending} onClick={() => void openSetup()}>{pendingModel ? "Continue setup" : "Set up"}</Button>}
      {status?.revocationPending && <p className="text-meta text-ink-2" role="status">Sharing is off. Nori will finish disconnecting when the network is available.</p>}
      {!available && <p className="text-meta text-ink-2">Home AI Server is unavailable. Try again later.</p>}
      {error && !stage && <Notice tone="danger">{error}</Notice>}
      {message && <p className="text-meta text-ink-2" role="status">{message}</p>}
    </div>
    <Dialog.Root open={!!stage} onOpenChange={(open) => { if (!open && !pending) setStage(undefined); }}>
      <Dialog.Portal>
        <Dialog.Overlay className="scrim fixed inset-0 z-50 bg-overlay" />
        <Dialog.Content className="anim-pop fixed top-1/2 left-1/2 z-50 w-[min(440px,calc(100vw-32px))] -translate-x-1/2 -translate-y-1/2 space-y-4 rounded-sheet border border-card-border bg-sheet p-6 shadow-pop"
          onEscapeKeyDown={(event) => { if (pending) event.preventDefault(); }}
          onPointerDownOutside={(event) => { if (pending) event.preventDefault(); }}>
          <Dialog.Title className="text-section font-semibold text-ink">
            {stage === "models" ? "Set up this Mac" : stage === "background" ? "Allow background access" : stage === "pair" ? "Connect this Mac" : "Choose where to use this model"}
          </Dialog.Title>
          <Dialog.Description className="text-meta text-ink-2">
            {stage === "models" ? "Choose an installed Ollama model. Nori can send model requests to this Mac while it is awake."
              : stage === "background" ? "Allow Nori to run in the background so your iPhone and scheduled Work can reach this Mac."
                : stage === "pair" ? "Connect this Mac to your Nori account to start sharing."
                : "You can change these choices later in Models."}
          </Dialog.Description>
          {stage === "models" && <>
            {models.length > 0 && <Field label="Ollama model"><select className={inputClass} value={model} onChange={(event) => setModel(event.target.value)} disabled={pending}>
              {models.map((item) => <option key={item.name} value={item.name}>{item.name}</option>)}
            </select></Field>}
            <div className="flex flex-wrap justify-end gap-2">
              <Dialog.Close asChild><Button size="sm" disabled={pending}>Cancel</Button></Dialog.Close>
              {models.length === 0 && <Button size="sm" disabled={pending} onClick={() => void openSetup()}>Check again</Button>}
              <Button size="sm" variant="primary" disabled={pending || !model} onClick={continueToBackground}>Continue</Button>
            </div>
          </>}
          {stage === "background" && <>
            {status?.background === "approval_required" && <Notice tone="warning">Allow Nori in System Settings → General → Login Items &amp; Extensions.</Notice>}
            <div className="flex flex-wrap justify-end gap-2">
              <Dialog.Close asChild><Button size="sm" disabled={pending}>Close</Button></Dialog.Close>
              <Button size="sm" variant="primary" disabled={pending} onClick={() => void enableBackground()}>
                {pending ? "Checking…" : status?.background === "approval_required" ? "Open System Settings" : "Allow background access"}
              </Button>
            </div>
          </>}
          {stage === "pair" && <div className="flex justify-end gap-2">
            <Dialog.Close asChild><Button size="sm" disabled={pending}>Close</Button></Dialog.Close>
            <Button size="sm" variant="primary" disabled={pending} onClick={() => void pair()}>{pending ? "Connecting…" : "Connect this Mac"}</Button>
          </div>}
          {stage === "routes" && <>
            <p className="text-row font-semibold">{model}</p>
            {!profile && <Notice tone="neutral">Waiting for this Mac to report its models. You can continue with your current choices.</Notice>}
            <label className="flex items-center gap-3 text-row"><input type="checkbox" className="accent-[var(--accent)]" checked={chat} disabled={!canChat || pending} onChange={(event) => setChat(event.target.checked)} /> Use for Chat</label>
            <label className="flex items-center gap-3 text-row"><input type="checkbox" className="accent-[var(--accent)]" checked={work} disabled={!canWork || pending} onChange={(event) => setWork(event.target.checked)} /> Use for Work</label>
            {profile && !canWork && <p className="text-meta text-ink-2">Work is unavailable for this model.</p>}
            <div className="flex justify-end gap-2">
              <Button size="sm" variant="primary" disabled={pending} onClick={() => void saveRoutes()}>{pending ? "Saving…" : "Done"}</Button>
            </div>
          </>}
          {error && <Notice tone="danger">{error}</Notice>}
        </Dialog.Content>
      </Dialog.Portal>
    </Dialog.Root>
  </>;
}
