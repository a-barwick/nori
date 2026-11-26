"use client";

import Link from "next/link";
import { usePathname, useRouter, useSearchParams } from "next/navigation";
import { Suspense, useCallback, useEffect, useRef, useState, type ReactNode } from "react";
import { MotionConfig } from "motion/react";
import { Tooltip } from "radix-ui";
import { ListChecks, LoaderCircle, MoreHorizontal } from "lucide-react";
import { NoriTabIcon } from "@/components/mark/NoriMark";
import { AppFrame } from "@/components/shell/AppFrame";
import { Button } from "@/components/ui/primitives";
import { useAppearance } from "@/lib/appearance";
import { AssistantProvider, useAssistant } from "@/lib/assistant/context";
import { APIError, getJSON, resetUserData } from "@/lib/assistant/api";
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
import { useSummary } from "@/lib/assistant/sources";
import { useResource } from "@/lib/assistant/useResource";
import { SIGNED_OUT_EVENT_KEY } from "@/lib/runtime/session";
import type { Preferences } from "@/lib/contracts/assistant";
import { cn } from "@/lib/utils";
import { OnboardingFlow, shouldShowOnboarding } from "./OnboardingFlow";
import { Badge, Failure, Loading, Notice } from "./ui";

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
      <MotionConfig reducedMotion="user">
        <AccountStateGate scope={scope} localMode={localMode}>
          <AssistantProvider localMode={localMode} scope={scope}>
            <Suspense fallback={null}>
              <Frame scope={scope}>{children}</Frame>
            </Suspense>
          </AssistantProvider>
        </AccountStateGate>
      </MotionConfig>
    </Tooltip.Provider>
  );
}

const ONBOARDING_STORAGE_PREFIX = "nori.onboarding.done:";
type AccountGateState = { status: "checking" | "ready" | "resetting" | "pending" | "complete" | "reset-error" | "error"; error?: Error };

function AccountStateGate({ scope, localMode, children }: { scope: string; localMode: boolean; children: ReactNode }) {
  const [state, setState] = useState<AccountGateState>({ status: "checking" });
  const [finishing, setFinishing] = useState(false);
  const [resumeError, setResumeError] = useState("");
  const activeCheck = useRef<AbortController | undefined>(undefined);
  const lastCheck = useRef<ResetCheckOptions>({});

  const checkReset = useCallback((options: ResetCheckOptions = {}) => {
    activeCheck.current?.abort();
    const controller = new AbortController();
    activeCheck.current = controller;
    lastCheck.current = options;
    void getJSON<Preferences>("/preferences", controller.signal).then((preferences) => {
      if (controller.signal.aborted) return;
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
      if (!controller.signal.aborted) setState({ status: "error", error });
    });
  }, [scope]);

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

  if (state.status === "error") return <main className="mx-auto max-w-xl space-y-4 p-6">
    <Failure error={state.error} onRetry={() => beginCheck(lastCheck.current)} />
    {!localMode && <form action="/api/auth/sign-out" method="post"><Button type="submit" variant="quiet">Sign out</Button></form>}
  </main>;
  if (state.status === "checking") return <main className="mx-auto max-w-xl p-6"><Loading /></main>;
  if (state.status === "resetting") return <main className="mx-auto max-w-xl p-6">
    <div role="status" className="flex items-center gap-3 text-body text-ink">
      <LoaderCircle className="size-5 animate-spin motion-reduce:animate-none" aria-hidden />Resetting data…
    </div>
  </main>;
  if (state.status === "pending") return <main className="mx-auto max-w-xl space-y-4 p-6">
    <h1 className="text-section font-semibold text-ink">Finish your data reset</h1>
    <p className="text-body text-ink-2">Your data reset is waiting to finish.</p>
    {resumeError && <Notice tone="danger">{resumeError}</Notice>}
    <Button variant="primary" disabled={finishing} onClick={() => void finishReset()}>{finishing ? "Finishing…" : "Finish reset"}</Button>
  </main>;
  if (state.status === "complete") return <main className="mx-auto max-w-xl space-y-4 p-6">
    <h1 className="text-section font-semibold text-ink">Data reset complete</h1>
    <p className="text-body text-ink-2">Your previous saved API keys and model choices were cleared. Continue to set up Nori.</p>
    <Button variant="primary" onClick={() => { acknowledgeResetCompletion(scope); setState({ status: "ready" }); }}>Continue</Button>
  </main>;
  if (state.status === "reset-error") return <main className="mx-auto max-w-xl space-y-4 p-6">
    <h1 className="text-section font-semibold text-ink">Data reset could not start</h1>
    <p className="text-body text-ink-2">Return to Reset data to try again.</p>
    <Button variant="primary" onClick={() => window.location.replace("/settings/reset")}>Return to Reset data</Button>
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
  const preferences = useResource<Preferences>("/preferences", { poll: 30_000, versioned: false });
  const path = usePathname();
  const router = useRouter();
  const searchParams = useSearchParams();
  const previous = useRef(path);
  const [localCompletion, setLocalCompletion] = useState(() => readLocalOnboardingDone(scope));
  const forceReplay = searchParams.get("onboarding") === "1";

  const handleOnboardingComplete = useCallback(() => {
    const completion = preferences.data?.data_reset_at ?? "1";
    setLocalCompletion(completion);
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
    messageCount: runtime.session?.messages?.length ?? 0,
    sessionLoaded: !runtime.loading && !!runtime.session,
    localCompleted: localCompletion === (preferences.data?.data_reset_at ?? "1"),
  });

  useEffect(() => {
    if (previous.current === path) return;
    previous.current = path;
    const frame = requestAnimationFrame(focusTitle);
    return () => cancelAnimationFrame(frame);
  }, [path]);

  const pending = summary.needs_you ?? 0;
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
      badgeLabel: "items needing action",
      match: (p) =>
        p.startsWith("/work") ||
        p.startsWith("/library") ||
        p.startsWith("/goals") ||
        p.startsWith("/routines"),
    },
    {
      href: "/more",
      label: "More",
      renderIcon: (_active, size) => <MoreHorizontal size={size} strokeWidth={1.85} aria-hidden />,
      dot: appsAttention > 0,
      dotLabel: `${appsAttention} apps require attention`,
      match: (p) =>
        p.startsWith("/more") ||
        p.startsWith("/apps") ||
        p.startsWith("/memory") ||
        p.startsWith("/settings"),
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
