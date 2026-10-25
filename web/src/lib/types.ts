export type ID = string;
export type Sender = "nori" | "me";
export type MarkState = "idle" | "thinking" | "listening" | "pleased";
export type MarkLook = "onigiri" | "sprout" | "pebble";

export interface Reaction {
  emoji: string;
  by: Sender;
}

export interface Chip {
  id: ID;
  /** Written as the user's own words, first person. */
  label: string;
}

export interface ChipSet {
  options: Chip[];
  chosenId?: ID;
}

interface BaseItem {
  id: ID;
  sender: Sender;
  at: number;
  reactions?: Reaction[];
  replyToId?: ID;
}

/** Markdown. The hybrid rule decides bubble vs cover + page at render time. */
export interface TextItem extends BaseItem {
  kind: "text";
  text: string;
  chips?: ChipSet;
  /** A made thing: renders as cover bubble + artifact tile. */
  artifactId?: ID;
  edited?: boolean;
  /** Links a bubble to the Activity entry that produced it. */
  activityId?: ID;
  /** Your bubble came from tapping this chip (drives the FLIP animation). */
  fromChip?: ID;
  /** Nori wrote first; counts toward the daily cap in Settings. */
  proactive?: boolean;
}

export interface VoiceItem extends BaseItem {
  kind: "voice";
  durationMs: number;
  waveform: number[];
  transcript?: string;
  audioUrl?: string;
}

export interface ImageItem extends BaseItem {
  kind: "image";
  src: string;
  alt: string;
  w: number;
  h: number;
}

export interface ApprovalItem extends BaseItem {
  kind: "approval";
  approvalId: ID;
}

/** Something you attached: a file or a Library item. */
export interface AttachmentItem extends BaseItem {
  kind: "attachment";
  artifactId: ID;
}

export type ThreadItem = TextItem | VoiceItem | ImageItem | ApprovalItem | AttachmentItem;

export interface Thread {
  id: ID;
  title: string;
  kind: "main" | "side";
  createdAt: number;
  items: ThreadItem[];
}

export type ActivityType = "done" | "web" | "memory" | "drafted" | "notified";

export interface ActivityEntry {
  id: ID;
  at: number;
  type: ActivityType;
  title: string;
  summary: string;
  inputs?: Record<string, string>;
  outputs?: Record<string, string>;
  threadId?: ID;
  messageId?: ID;
}

export interface Approval {
  id: ID;
  at: number;
  /** Clear verb phrase: "Send a message to the seller". */
  title: string;
  what: string;
  why: string;
  impact: string;
  status: "pending" | "approved" | "declined";
  resolvedAt?: number;
  outcome?: string;
  threadId: ID;
  messageId?: ID;
}

export type UpcomingCadence = "daily" | "week" | "later";
export type UpcomingGlyph = "heart" | "eye" | "sun" | "calendar" | "package" | "printer" | "gift" | "receipt";

export type Repeat =
  | { every: number }
  | { daily: string; weekdays?: boolean }
  | { weekly: number; at: string };

export interface UpcomingTask {
  id: ID;
  title: string;
  cadence: UpcomingCadence;
  schedule: string;
  nextRunAt: number;
  /** Absent for one-off tasks. */
  repeat?: Repeat;
  glyph: UpcomingGlyph;
  paused?: boolean;
}

export type IdeaGroup = "For you" | "Around the house" | "Shopping";

export interface Idea {
  id: ID;
  group: IdeaGroup;
  emoji: string;
  title: string;
  description: string;
  /** What Nori says when you start it in chat. */
  pitch: string;
  chips: Chip[];
  dismissed?: "not-now" | "fewer";
}

export type GoalStatus = "tracking" | "paused" | "done";
export type GoalCategory =
  | "health"
  | "relationships"
  | "finance"
  | "career"
  | "interests"
  | "productivity"
  | "other";

export interface GoalEvent {
  at: number;
  text: string;
}

export interface Goal {
  id: ID;
  title: string;
  status: GoalStatus;
  category: GoalCategory;
  line: string;
  blocked?: { reason: string; action: string };
  nextStep: string;
  progress: GoalEvent[];
  history: GoalEvent[];
  talkPitch: string;
  talkChips: Chip[];
}

export type ArtifactType = "document" | "page" | "dashboard" | "image" | "audio" | "file";

export interface Artifact {
  id: ID;
  title: string;
  type: ArtifactType;
  createdAt: number;
  madeBy: "nori" | "you";
  /** Markdown body for documents and pages. */
  body?: string;
  src?: string;
  /** Drives the drawn thumbnail. */
  thumb?: "doc" | "list" | "table" | "page" | "dash" | "image" | "audio" | "file";
  sizeLabel?: string;
  durationMs?: number;
}

export interface IdentityRevision {
  at: number;
  before: string;
  after: string;
  note: string;
}

export interface IdentityDoc {
  content: string;
  updatedAt: number;
  history: IdentityRevision[];
}

export interface Identity {
  name: string;
  look: MarkLook;
  soul: IdentityDoc;
  memory: IdentityDoc;
}

export interface Settings {
  proactive: boolean;
  quietStart: string;
  quietEnd: string;
  dailyCap: number;
}

export type PanelTab = "activity" | "approvals" | "upcoming" | "identity";

export interface WorkStatus {
  label: string;
  activityId?: ID;
  threadId: ID;
}
