"use client";

import { create } from "zustand";
import { createJSONStorage, persist } from "zustand/middleware";
import { createSeed, MAIN_THREAD, type Seed } from "./seed";
import { uid } from "./utils";
import type {
  ActivityEntry,
  Approval,
  Artifact,
  ArtifactType,
  ID,
  IdentityDoc,
  MarkLook,
  MarkState,
  PanelTab,
  Settings,
  Thread,
  ThreadItem,
  UpcomingTask,
  WorkStatus,
} from "./types";

export type LibraryFilter = "all" | "document" | "page" | "dashboard" | "image" | "audio" | "file";

export interface ReplyTarget {
  threadId: ID;
  itemId?: ID;
  artifactId?: ID;
}

interface UIState {
  /** Docked panel (≥1280px): separate defaults for Chat and other pages. */
  dock: { chat: boolean; pages: boolean };
  /** Overlay sheet (<1280px). */
  overlay: boolean;
  panelTab: PanelTab;
  focusActivityId: ID | null;
  drawerOpen: boolean;
  searchOpen: boolean;
  shortcutsOpen: boolean;
  reportOpen: boolean;
  activeThreadId: ID;
  highlight: { itemId: ID; nonce: number } | null;
  goalSheetId: ID | null;
  identitySheet: "soul" | "memory" | null;
  editLookOpen: boolean;
  libraryFilter: LibraryFilter;
  viewerId: ID | null;
  replyTo: ReplyTarget | null;
  editingId: ID | null;
  drafts: Record<ID, string>;
  markState: MarkState;
  typing: ID | null;
  work: WorkStatus | null;
  liveError: { threadId: ID; message: string } | null;
  /** A live reply as it's being written, revealed word by word. */
  stream: { threadId: ID; text: string } | null;
  lightbox: { src: string; alt: string } | null;
  composerFocus: number;
  /** Items that arrived after load animate in. */
  arrived: Record<ID, true>;
}

interface Domain extends Seed {
  version: number;
}

interface Actions {
  // threads
  appendItem: (threadId: ID, item: ThreadItem, opts?: { animate?: boolean }) => void;
  updateItem: (threadId: ID, itemId: ID, patch: Partial<ThreadItem>) => void;
  removeItem: (threadId: ID, itemId: ID) => void;
  chooseChip: (threadId: ID, itemId: ID, chipId: ID) => void;
  toggleReaction: (threadId: ID, itemId: ID, emoji: string) => void;
  newSideChat: () => ID;
  renameThread: (id: ID, title: string) => void;
  setActiveThread: (id: ID) => void;
  // panel + layers
  togglePanel: (context: "chat" | "pages", wide: boolean) => void;
  setDock: (context: "chat" | "pages", open: boolean) => void;
  setOverlay: (open: boolean) => void;
  openPanelAt: (tab: PanelTab, activityId?: ID | null) => void;
  setPanelTab: (tab: PanelTab) => void;
  setFocusActivity: (id: ID | null) => void;
  set: (patch: Partial<UIState>) => void;
  // work
  addActivity: (entry: Omit<ActivityEntry, "id" | "at"> & { id?: ID; at?: number }) => ID;
  linkActivity: (id: ID, threadId: ID, messageId: ID) => void;
  resolveApproval: (id: ID, approve: boolean, outcome?: string) => void;
  addApproval: (a: Omit<Approval, "id" | "at" | "status">) => ID;
  addArtifact: (a: Omit<Artifact, "id" | "createdAt"> & { id?: ID }) => ID;
  addUpcoming: (t: UpcomingTask) => void;
  togglePauseUpcoming: (id: ID) => void;
  dismissIdea: (id: ID, kind: "not-now" | "fewer") => void;
  // identity
  saveIdentityDoc: (kind: "soul" | "memory", content: string, note: string) => void;
  setNameAndLook: (name: string, look: MarkLook) => void;
  // settings
  updateSettings: (patch: Partial<Settings>) => void;
  resetDemo: () => void;
  // drafts
  setDraft: (threadId: ID, text: string) => void;
  highlightItem: (itemId: ID) => void;
}

export type NoriStore = Domain & UIState & Actions;

const SEED_VERSION = 7;

const freshDomain = (): Domain => ({ ...createSeed(Date.now()), version: SEED_VERSION });

const initialUI = (): UIState => ({
  dock: { chat: false, pages: false },
  overlay: false,
  panelTab: "activity",
  focusActivityId: null,
  drawerOpen: false,
  searchOpen: false,
  shortcutsOpen: false,
  reportOpen: false,
  activeThreadId: MAIN_THREAD,
  highlight: null,
  goalSheetId: null,
  identitySheet: null,
  editLookOpen: false,
  libraryFilter: "all",
  viewerId: null,
  replyTo: null,
  editingId: null,
  drafts: {},
  markState: "idle",
  typing: null,
  work: null,
  liveError: null,
  stream: null,
  lightbox: null,
  composerFocus: 0,
  arrived: {},
});

const mapThread = (threads: Thread[], id: ID, fn: (t: Thread) => Thread) =>
  threads.map((t) => (t.id === id ? fn(t) : t));

export const useNori = create<NoriStore>()(
  persist(
    (set) => ({
      ...freshDomain(),
      ...initialUI(),

      appendItem: (threadId, item, opts) =>
        set((s) => ({
          threads: mapThread(s.threads, threadId, (t) => ({ ...t, items: [...t.items, item] })),
          arrived: opts?.animate === false ? s.arrived : { ...s.arrived, [item.id]: true },
        })),

      updateItem: (threadId, itemId, patch) =>
        set((s) => ({
          threads: mapThread(s.threads, threadId, (t) => ({
            ...t,
            items: t.items.map((i) => (i.id === itemId ? ({ ...i, ...patch } as ThreadItem) : i)),
          })),
        })),

      removeItem: (threadId, itemId) =>
        set((s) => ({
          threads: mapThread(s.threads, threadId, (t) => ({ ...t, items: t.items.filter((i) => i.id !== itemId) })),
        })),

      chooseChip: (threadId, itemId, chipId) =>
        set((s) => ({
          threads: mapThread(s.threads, threadId, (t) => ({
            ...t,
            items: t.items.map((i) =>
              i.id === itemId && i.kind === "text" && i.chips ? { ...i, chips: { ...i.chips, chosenId: chipId } } : i,
            ),
          })),
        })),

      toggleReaction: (threadId, itemId, emoji) =>
        set((s) => ({
          threads: mapThread(s.threads, threadId, (t) => ({
            ...t,
            items: t.items.map((i) => {
              if (i.id !== itemId) return i;
              const mine = i.reactions?.find((r) => r.by === "me");
              const others = (i.reactions ?? []).filter((r) => r.by !== "me");
              const next = mine?.emoji === emoji ? others : [...others, { emoji, by: "me" as const }];
              return { ...i, reactions: next };
            }),
          })),
        })),

      newSideChat: () => {
        const id = uid("side");
        const now = Date.now();
        set((s) => ({
          threads: [...s.threads, { id, title: "New side chat", kind: "side", createdAt: now, items: [] }],
          activeThreadId: id,
          drawerOpen: false,
        }));
        return id;
      },

      renameThread: (id, title) => set((s) => ({ threads: mapThread(s.threads, id, (t) => ({ ...t, title })) })),

      setActiveThread: (id) => set({ activeThreadId: id, replyTo: null, editingId: null }),

      togglePanel: (context, wide) =>
        set((s) => (wide ? { dock: { ...s.dock, [context]: !s.dock[context] } } : { overlay: !s.overlay })),
      setDock: (context, open) => set((s) => ({ dock: { ...s.dock, [context]: open } })),
      setOverlay: (open) => set({ overlay: open }),

      openPanelAt: (tab, activityId = null) => {
        const wide = typeof window !== "undefined" && window.innerWidth >= 1280;
        const onChat = typeof window !== "undefined" && window.location.pathname === "/";
        set((s) => ({
          panelTab: tab,
          focusActivityId: activityId,
          ...(wide ? { dock: { ...s.dock, [onChat ? "chat" : "pages"]: true } } : { overlay: true }),
        }));
      },

      setPanelTab: (tab) => set({ panelTab: tab }),
      setFocusActivity: (id) => set({ focusActivityId: id }),
      set: (patch) => set(patch),

      addActivity: (entry) => {
        const id = entry.id ?? uid("a");
        set((s) => ({ activity: [{ ...entry, id, at: entry.at ?? Date.now() }, ...s.activity] }));
        return id;
      },

      linkActivity: (id, threadId, messageId) =>
        set((s) => ({ activity: s.activity.map((a) => (a.id === id ? { ...a, threadId, messageId } : a)) })),

      resolveApproval: (id, approve, outcome) =>
        set((s) => ({
          approvals: s.approvals.map((a) =>
            a.id === id
              ? {
                  ...a,
                  status: approve ? "approved" : "declined",
                  resolvedAt: Date.now(),
                  outcome: outcome ?? (approve ? "Approved · sent" : "Not now · kept as a draft"),
                }
              : a,
          ),
        })),

      addApproval: (a) => {
        const id = uid("ap");
        set((s) => ({ approvals: [{ ...a, id, at: Date.now(), status: "pending" }, ...s.approvals] }));
        return id;
      },

      addArtifact: (a) => {
        const id = a.id ?? uid("art");
        set((s) => ({ artifacts: [{ ...a, id, createdAt: Date.now() }, ...s.artifacts] }));
        return id;
      },

      addUpcoming: (t) => set((s) => ({ upcoming: [t, ...s.upcoming.filter((u) => u.id !== t.id)] })),

      togglePauseUpcoming: (id) =>
        set((s) => ({ upcoming: s.upcoming.map((u) => (u.id === id ? { ...u, paused: !u.paused } : u)) })),

      dismissIdea: (id, kind) => set((s) => ({ ideas: s.ideas.map((i) => (i.id === id ? { ...i, dismissed: kind } : i)) })),

      saveIdentityDoc: (kind, content, note) =>
        set((s) => {
          const doc: IdentityDoc = s.identity[kind];
          if (doc.content === content) return {};
          const at = Date.now();
          return {
            identity: {
              ...s.identity,
              [kind]: {
                content,
                updatedAt: at,
                history: [{ at, before: doc.content, after: content, note }, ...doc.history],
              },
            },
          };
        }),

      setNameAndLook: (name, look) => set((s) => ({ identity: { ...s.identity, name: name.trim() || "Nori", look } })),

      updateSettings: (patch) => set((s) => ({ settings: { ...s.settings, ...patch } })),

      resetDemo: () => set({ ...freshDomain(), ...initialUI() }),

      setDraft: (threadId, text) => set((s) => ({ drafts: { ...s.drafts, [threadId]: text } })),

      highlightItem: (itemId) => set({ highlight: { itemId, nonce: Date.now() } }),
    }),
    {
      name: "nori-demo",
      version: SEED_VERSION,
      // A full localStorage must never break the app: saving is best effort.
      storage: createJSONStorage(() => ({
        getItem: (k) => localStorage.getItem(k),
        setItem: (k, v) => {
          try {
            localStorage.setItem(k, v);
          } catch (err) {
            console.warn("Nori: couldn't save demo state", err);
          }
        },
        removeItem: (k) => localStorage.removeItem(k),
      })),
      migrate: () => freshDomain() as unknown as NoriStore,
      partialize: (s): Domain => ({
        version: s.version,
        threads: s.threads.map((t) => ({
          ...t,
          items: t.items.map((i) => {
            if (i.kind === "voice" && i.audioUrl?.startsWith("blob:")) return { ...i, audioUrl: undefined };
            // Your photos stay in memory only; seeded images are small static files.
            if (i.kind === "image" && (i.src.startsWith("blob:") || (i.src.startsWith("data:") && !i.src.startsWith("data:image/svg+xml")))) return { ...i, src: "" };
            return i;
          }),
        })),
        activity: s.activity,
        approvals: s.approvals,
        upcoming: s.upcoming,
        ideas: s.ideas,
        goals: s.goals,
        artifacts: s.artifacts,
        identity: s.identity,
        settings: s.settings,
      }),
    },
  ),
);

export const getNori = () => useNori.getState();

export function useThread(id: ID) {
  return useNori((s) => s.threads.find((t) => t.id === id));
}

export const pendingApprovals = (s: NoriStore) => s.approvals.filter((a) => a.status === "pending");

export const artifactTypeLabel: Record<ArtifactType, string> = {
  document: "Document",
  page: "Page",
  dashboard: "Dashboard",
  image: "Image",
  audio: "Audio",
  file: "File",
};
