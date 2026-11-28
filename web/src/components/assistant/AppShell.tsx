"use client";

import dynamic from "next/dynamic";
import Link from "next/link";
import { usePathname, useRouter, useSearchParams } from "next/navigation";
import { Suspense, useCallback, useContext, useEffect, useRef, useState, type ReactNode } from "react";
import { Tooltip } from "radix-ui";
import { ListChecks, LoaderCircle, MoreHorizontal } from "lucide-react";
import { NoriTabIcon } from "@/components/mark/NoriMark";
import { AppFrame } from "@/components/shell/AppFrame";
import { Button } from "@/components/ui/primitives";
import { useAppearance } from "@/lib/appearance";
import { AssistantProvider, ResourceCacheProvider, useAssistant } from "@/lib/assistant/context";
import { ACCOUNT_DELETED_REDIRECT, APIError, deleteAccount, errorText, getJSON, resetUserData } from "@/lib/assistant/api";
import { shouldShowOnboarding } from "@/lib/assistant/onboarding";
import { useHydrated } from "@/lib/hooks";
import {
  acknowledgeResetCompletion,
  browserResetChanged,
  markBrowserReset,
  requestResetCheck,
  resetCompletionPending,
  resetInProgress,
  resetMarkerKey,
  RESET_CHECK_EVENT,
  RESET_CHECK_KEY,
  RESET_STARTED_EVENT,
  syncBrowserReset,
  type ResetCheckOptions,
} from "@/lib/assistant/resetState";
import { isActionableInteraction } from "@/lib/assistant/interactions";
import { usePendingInteractions, useSummary } from "@/lib/assistant/sources";
import { ResourceCacheContext, useResource } from "@/lib/assistant/useResource";
import { SIGNED_OUT_EVENT_KEY } from "@/lib/runtime/session";
import type { Preferences } from "@/lib/contracts/assistant";
import { cn } from "@/lib/utils";
import { SignOutForm } from "./SignOutForm";
import { Badge, Failure, inputClass, Loading, Notice } from "./ui";

const OnboardingFlow = dynamic(
  () => import("./OnboardingFlow").then((mod) => mod.OnboardingFlow),
  { ssr: false },
);

type NavItem = {
  href: string;
  label: string;
  renderIcon: (active: boolean, size: number) => ReactNode;
  badge?: number;
  badgeLabel?: string;
  dot?: boolean;
  dotLabel?: string;
  match: (path: string) => boolean;
};

export function AppShell({ localMode, scope, children }: { localMode: boolean; scope: string; children: ReactNode }) {
  useAppearance();
  return (
    <Tooltip.Provider delayDuration={500}>
      <ResourceCacheProvider scope={scope}>
        <AccountStateGate scope={scope} localMode={localMode}>
          <AssistantProvider localMode={localMode} scope={scope}>
            <Suspense fallback={null}>
              <Frame scope={scope}>{children}</Frame>
            </Suspense>
          </AssistantProvider>
        </AccountStateGate>
      </ResourceCacheProvider>
    </Tooltip.Provider>
  );
}

const ONBOARDING_STORAGE_PREFIX = "nori.onboarding.done:";
type AccountGateState = { status: "checking" | "ready" | "resetting" | "pending" | "delete-pending" | "complete" | "reset-error" | "error"; error?: Error };

function AccountStateGate({ scope, localMode, children }: { scope: string; localMode: boolean; children: ReactNode }) {
  const cache = useContext(ResourceCacheContext);
  const [state, setState] = useState<AccountGateState>({ status: "checking" });
  const [finishing, setFinishing] = useState(false);
  const [resumeError, setResumeError] = useState("");
  const [deleteEmail, setDeleteEmail] = useState("");
  const activeCheck = useRef<AbortController | undefined>(undefined);
  const lastCheck = useRef<ResetCheckOptions>({});

  const checkReset = useCallback((options: ResetCheckOptions = {}) => {
    activeCheck.current?.abort();
    const controller = new AbortController();
    activeCheck.current = controller;
    lastCheck.current = options;
    void getJSON<Preferences>("/preferences", controller.signal).then((preferences) => {
      if (controller.signal.aborted) return;
      cache.seed("/preferences", preferences, false);
      syncBrowserReset(scope, preferences.data_reset_at);
      if (preferences.data_reset_pending) {
        setState({ status: "pending" });
      } else if (options.suppressCompletion) {
        acknowledgeResetCompletion(scope);
        setState({ status: "ready" });
      } else if (!preferences.onboarding_completed_at && resetCompletionPending(scope, preferences.data_reset_at)) {
        setState({ status: "complete" });
      } else if (options.resetFailed) {
        setState({ status: "reset-error" });
      } else {
        setState({ status: "ready" });
      }
    }).catch((error: Error) => {
      if (controller.signal.aborted) return;
      if (error instanceof APIError && error.code === "account_delete_pending") {
        setState({ status: "delete-pending" });
        return;
      }
      setState({ status: "error", error });
    });
  }, [cache, scope]);

  const beginCheck = useCallback((options: ResetCheckOptions = {}) => {
    setState({ status: "checking" });
    checkReset(options);
  }, [checkReset]);

  useEffect(() => {
    checkReset();
    const reload = () => { if (!resetInProgress()) window.location.replace("/"); };
    const onResetStarted = () => { activeCheck.current?.abort(); setState({ status: "resetting" }); };
    const onResetCheck = (event: Event) => { if (!resetInProgress()) beginCheck((event as CustomEvent<ResetCheckOptions>).detail); };
    const onStorage = (event: StorageEvent) => {
      if (event.key === resetMarkerKey(scope) && event.oldValue !== event.newValue) reload();
      if (event.key === SIGNED_OUT_EVENT_KEY && event.oldValue !== event.newValue) window.location.replace("/signed-out");
      if (event.key === RESET_CHECK_KEY && event.oldValue !== event.newValue && !resetInProgress()) beginCheck();
    };
    window.addEventListener("nori:data-reset", reload);
    window.addEventListener(RESET_CHECK_EVENT, onResetCheck);
    window.addEventListener(RESET_STARTED_EVENT, onResetStarted);
    window.addEventListener("storage", onStorage);
    return () => {
      activeCheck.current?.abort();
      window.removeEventListener("nori:data-reset", reload);
      window.removeEventListener(RESET_CHECK_EVENT, onResetCheck);
      window.removeEventListener(RESET_STARTED_EVENT, onResetStarted);
      window.removeEventListener("storage", onStorage);
    };
  }, [beginCheck, checkReset, scope]);

  const finishReset = async () => {
    if (finishing) return;
    setFinishing(true);
    setResumeError("");
    try {
      await resetUserData(true);
      markBrowserReset(scope);
      window.location.replace("/");
    } catch (error) {
      if (!(error instanceof APIError && error.code === "data_reset_not_pending")) {
        setResumeError("Nori couldn’t finish the reset. Try again.");
      }
      setFinishing(false);
    }
  };

  if (state.status === "error") return <main className="h-full overflow-y-auto bg-canvas p-6">
    <div className="mx-auto max-w-xl space-y-4">
      <Failure error={state.error} onRetry={() => beginCheck(lastCheck.current)} />
      {!localMode && <SignOutForm />}
    </div>
  </main>;
  if (state.status === "checking") return <main className="h-full overflow-y-auto bg-canvas p-6">
    <div className="mx-auto max-w-xl"><Loading /></div>
  </main>;
  if (state.status === "resetting") return <main className="h-full overflow-y-auto bg-canvas p-6">
    <div role="status" className="mx-auto flex max-w-xl items-center gap-3 text-body text-ink">
      <LoaderCircle className="size-5 animate-spin motion-reduce:animate-none" aria-hidden />Resetting data…
    </div>
  </main>;
  const finishAccountDelete = async () => {
    if (finishing) return;
    const confirmation = deleteEmail.trim();
    if (!confirmation) {
      setResumeError("Enter your account email to finish deleting your account.");
      return;
    }
    setFinishing(true);
    setResumeError("");
    try {
      await deleteAccount(confirmation, true);
      window.location.replace(ACCOUNT_DELETED_REDIRECT);
    } catch (error) {
      setResumeError(error instanceof APIError ? errorText(error) : "Nori could not finish deleting your account. Try again.");
      setFinishing(false);
    }
  };

  if (state.status === "delete-pending") return <main className="h-full overflow-y-auto bg-canvas p-6">
    <div className="mx-auto max-w-xl space-y-4">
      <h1 className="text-section font-semibold text-ink">Finish deleting your account</h1>
      <p className="text-body text-ink-2">Your account deletion was interrupted. Enter your account email to finish removing your account and data.</p>
      {resumeError && <Notice tone="danger">{resumeError}</Notice>}
      <form
        onSubmit={(event) => {
          event.preventDefault();
          void finishAccountDelete();
        }}
        className="space-y-4"
      >
        <div>
          <label htmlFor="delete-account-email" className="sr-only">Account email</label>
          <input
            id="delete-account-email"
            type="email"
            value={deleteEmail}
            onChange={(event) => setDeleteEmail(event.target.value)}
            placeholder="Account email"
            className={inputClass}
            autoComplete="email"
            disabled={finishing}
          />
        </div>
        <Button type="submit" variant="danger" disabled={finishing}>
          {finishing ? "Deleting…" : "Finish deleting account"}
        </Button>
      </form>
      {!localMode && <SignOutForm />}
    </div>
  </main>;
  if (state.status === "pending") return <main className="h-full overflow-y-auto bg-canvas p-6">
    <div className="mx-auto max-w-xl space-y-4">
      <h1 className="text-section font-semibold text-ink">Finish your data reset</h1>
      <p className="text-body text-ink-2">Your data reset is waiting to finish.</p>
      {resumeError && <Notice tone="danger">{resumeError}</Notice>}
      <Button variant="primary" disabled={finishing} onClick={() => void finishReset()}>{finishing ? "Finishing…" : "Finish reset"}</Button>
      {!localMode && <SignOutForm />}
    </div>
  </main>;
  if (state.status === "complete") return <main className="h-full overflow-y-auto bg-canvas p-6">
    <div className="mx-auto max-w-xl space-y-4">
      <h1 className="text-section font-semibold text-ink">Data reset complete</h1>
      <p className="text-body text-ink-2">Your previous saved API keys and model choices were cleared. Continue to set up Nori.</p>
      <Button variant="primary" onClick={() => { acknowledgeResetCompletion(scope); setState({ status: "ready" }); }}>Continue</Button>
      {!localMode && <SignOutForm />}
    </div>
  </main>;
  if (state.status === "reset-error") return <main className="h-full overflow-y-auto bg-canvas p-6">
    <div className="mx-auto max-w-xl space-y-4">
      <h1 className="text-section font-semibold text-ink">Data reset could not start</h1>
      <p className="text-body text-ink-2">Return to Reset data to try again.</p>
      <Button variant="primary" onClick={() => window.location.replace("/settings/reset")}>Return to Reset data</Button>
      {!localMode && <SignOutForm />}
    </div>
  </main>;
  return children;
}

function readLocalOnboardingDone(scope: string): string | null {
  if (typeof window === "undefined") return null;
  try {
    return window.localStorage.getItem(`${ONBOARDING_STORAGE_PREFIX}${scope}`);
  } catch {
    return null;
  }
}

function Frame({ scope, children }: { scope: string; children: ReactNode }) {
  const { runtime } = useAssistant();
  const summary = useSummary();
  const pendingInteractions = usePendingInteractions();
  const preferences = useResource<Preferences>("/preferences", { poll: 30_000, versioned: false });
  const path = usePathname();
  const router = useRouter();
  const searchParams = useSearchParams();
  const hydrated = useHydrated();
  const previous = useRef(path);
  const [localCompletionOverride, setLocalCompletionOverride] = useState<string | null | undefined>();
  const localCompletion = localCompletionOverride !== undefined ? localCompletionOverride : (hydrated ? readLocalOnboardingDone(scope) : null);
  const forceReplay = searchParams.get("onboarding") === "1";

  const handleOnboardingComplete = useCallback(() => {
    const completion = preferences.data?.data_reset_at ?? "1";
    setLocalCompletionOverride(completion);
    try {
      window.localStorage.setItem(`${ONBOARDING_STORAGE_PREFIX}${scope}`, completion);
    } catch {
    }
    if (forceReplay) {
      router.replace("/");
    }
  }, [forceReplay, preferences.data?.data_reset_at, router, scope]);

  useEffect(() => {
    if (preferences.data?.data_reset_pending) {
      requestResetCheck();
    } else if (preferences.data && browserResetChanged(scope, preferences.data.data_reset_at)) {
      syncBrowserReset(scope, preferences.data.data_reset_at);
      window.dispatchEvent(new Event("nori:data-reset"));
    }
  }, [preferences.data, scope]);

  const showOnboarding = shouldShowOnboarding({
    forceReplay,
    preferences: preferences.data,
    preferencesLoaded: !preferences.loading && (!!preferences.data || !!preferences.error),
    session: runtime.session ?? undefined,
    sessionLoaded: !runtime.loading && !!runtime.session,
    localCompleted: localCompletion === (preferences.data?.data_reset_at ?? "1"),
  });

  useEffect(() => {
    if (previous.current === path) return;
    previous.current = path;
    const frame = requestAnimationFrame(focusTitle);
    return () => cancelAnimationFrame(frame);
  }, [path]);

  const pending = pendingInteractions.items
    ? pendingInteractions.items.filter(isActionableInteraction).length
    : (summary.needs_you ?? 0);
  const appsAttention = summary.apps_attention ?? 0;
  const { busy } = runtime;

  const items: NavItem[] = [
    {
      href: "/",
      label: "Chat",
      renderIcon: (active, size) => <NoriTabIcon active={active} size={size} />,
      match: (p) => p === "/",
    },
    {
      href: "/work",
      label: "Work",
      renderIcon: (_active, size) => <ListChecks size={size} strokeWidth={1.85} aria-hidden />,
      badge: pending,
      badgeLabel: pending === 1 ? "item needing action" : "items needing action",
      match: (p) =>
        p.startsWith("/work") ||
        p.startsWith("/library") ||
        p.startsWith("/routines") ||
        p.startsWith("/computer"),
    },
    {
      href: "/more",
      label: "More",
      renderIcon: (_active, size) => <MoreHorizontal size={size} strokeWidth={1.85} aria-hidden />,
      dot: appsAttention > 0,
      dotLabel: appsAttention === 1 ? "1 app requires attention" : `${appsAttention} apps require attention`,
      match: (p) =>
        p.startsWith("/more") ||
        p.startsWith("/apps") ||
        p.startsWith("/memory") ||
        p.startsWith("/settings") ||
        p.startsWith("/connector"),
    },
  ];

  const onNavClick = (item: NavItem, active: boolean) => (event: React.MouseEvent<HTMLAnchorElement>) => {
    if (active && item.href === "/") {
      event.preventDefault();
      window.dispatchEvent(new CustomEvent("nori:chat-scroll-bottom"));
    }
  };

  const desktopNav = (
    <nav aria-label="Main" className="nori-pill-nav">
      {items.map((item) => {
        const active = item.match(path);
        return (
          <Link
            key={item.href}
            href={item.href}
            onClick={onNavClick(item, active)}
            aria-current={active ? "page" : undefined}
            className="nori-pill-nav-item"
          >
            {item.renderIcon(active, 18)}
            <span>{item.label}</span>
            {item.badge ? <Badge count={item.badge} label={item.badgeLabel ?? ""} /> : null}
            {!item.badge && item.dot ? (
              <span
                aria-label={item.dotLabel}
                className="size-[6.5px] shrink-0 rounded-full bg-accent"
              />
            ) : null}
          </Link>
        );
      })}
    </nav>
  );

  const mobileTabs = (
    <nav aria-label="Main" className="nori-tabbar flex shrink-0 border-t border-hairline bg-surface sm:hidden">
      {items.map((item) => {
        const active = item.match(path);
        return (
          <Link
            key={item.href}
            href={item.href}
            onClick={onNavClick(item, active)}
            aria-current={active ? "page" : undefined}
            className={cn("nori-tab", active && "text-accent-text")}
          >
            <span className="relative">
              {item.renderIcon(active, 22)}
              {item.badge ? (
                <span className="absolute -top-1.5 -right-2.5">
                  <Badge count={item.badge} label={item.badgeLabel ?? ""} />
                </span>
              ) : item.dot ? (
                <span
                  aria-label={item.dotLabel}
                  className="absolute -top-0.5 -right-1.5 size-2 rounded-full bg-accent"
                />
              ) : null}
            </span>
            <span>{item.label}</span>
          </Link>
        );
      })}
    </nav>
  );

  if (showOnboarding && path !== "/connector/activate") {
    return <OnboardingFlow preferences={preferences.data} onComplete={handleOnboardingComplete} />;
  }

  return (
    <>
      <a
        href="#content"
        onClick={(event) => {
          event.preventDefault();
          skipToContent();
        }}
        className="sr-only z-50 rounded-full bg-surface px-4 py-2 text-row-title font-medium text-accent-text shadow-pill focus:not-sr-only focus:fixed focus:top-3 focus:left-3"
      >
        Skip to content
      </a>
      <AppFrame state={busy ? "thinking" : "idle"} nav={desktopNav} footer={mobileTabs}>
        {children}
        <div id="nori-announcer" role="status" aria-live="polite" className="sr-only" />
      </AppFrame>
    </>
  );
}

function skipToContent() {
  const composer = [...document.querySelectorAll<HTMLElement>('main textarea[aria-label="Message Nori"]')].find(
    (element) => element.getClientRects().length > 0,
  );
  if (composer) composer.focus();
  else focusTitle();
}

function focusTitle() {
  const heading = [...document.querySelectorAll<HTMLElement>("main h1")].find(
    (element) => element.offsetParent !== null || element.getClientRects().length > 0,
  );
  if (!heading) return;
  heading.tabIndex = -1;
  heading.focus({ preventScroll: true });
}
