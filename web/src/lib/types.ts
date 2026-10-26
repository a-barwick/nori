export type Sender = "nori" | "me";
export type MarkState = "idle" | "attentive" | "thinking" | "writing" | "listening" | "pleased";
export type MarkLook = "onigiri" | "sprout" | "pebble";

/** Saved runtime message; markdown structure selects bubble or document rendering. */
export interface TextItem {
  id: string;
  sender: Sender;
  at: number;
  kind: "text";
  text: string;
  debug?: import("@/components/dev/debug").DebugInfo;
}

export type ThreadItem = TextItem;
export interface Thread {
  id: string;
  title: string;
  kind: "main";
  createdAt: number;
  items: ThreadItem[];
}
