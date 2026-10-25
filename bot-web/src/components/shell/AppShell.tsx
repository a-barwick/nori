"use client";

import { usePathname } from "next/navigation";
import { MotionConfig } from "motion/react";
import { Tooltip } from "radix-ui";
import { useHydrated, useWide } from "@/lib/hooks";
import { useNori } from "@/lib/store";
import { Rail } from "./Rail";
import { AgentPanel, PanelOverlay } from "@/components/panel/AgentPanel";
import { SearchPalette } from "./SearchPalette";
import { ShortcutsDialog, ReportDialog } from "./Dialogs";
import { useGlobalShortcuts } from "./useGlobalShortcuts";
import { GoalSheet } from "@/components/goals/GoalSheet";
import { IdentitySheet, EditLookDialog } from "@/components/panel/IdentitySheets";
import { Lightbox } from "@/components/chat/Lightbox";

export function AppShell({ children }: { children: React.ReactNode }) {
  const hydrated = useHydrated();
  // The app reads the clock, the viewport, and saved state, so it renders on the client.
  if (!hydrated) return <div className="fixed inset-0 bg-canvas" />;
  return <Shell>{children}</Shell>;
}

function Shell({ children }: { children: React.ReactNode }) {
  const onChat = usePathname() === "/";
  const wide = useWide();
  const dockOpen = useNori((s) => s.dock[onChat ? "chat" : "pages"]);
  const docked = wide && dockOpen;
  useGlobalShortcuts();

  return (
    <Tooltip.Provider delayDuration={500} skipDelayDuration={250}>
      <MotionConfig reducedMotion="user">
        {/* clip, not hidden: nothing (focus, scrollIntoView) can scroll the shell itself */}
        <div className="fixed inset-0 flex overflow-clip bg-canvas text-ink">
          <Rail />
          <main className="relative min-w-0 flex-1 overflow-clip">{children}</main>
          <aside
            aria-label="Agent panel"
            inert={!docked}
            className="hairline-l h-full shrink-0 overflow-clip transition-[width] ease-spring"
            style={{ width: docked ? "var(--panel-w)" : 0, transitionDuration: "var(--dur-spring)" }}
          >
            <div className="h-full" style={{ width: "var(--panel-w)" }}>
              <AgentPanel instance="dock" />
            </div>
          </aside>
        </div>
        {!wide && <PanelOverlay />}
        <SearchPalette />
        <ShortcutsDialog />
        <ReportDialog />
        <GoalSheet />
        <IdentitySheet />
        <EditLookDialog />
        <Lightbox />
      </MotionConfig>
    </Tooltip.Provider>
  );
}
