import type { Message, Run, Session } from "@/lib/poc/types";

// Debug info shown under a Nori message when debug mode is on. The runtime
// records only run status and event names today, so the step detail here is a
// static sample until the service keeps model and tool records.

export type DebugStep =
  | {
      kind: "model";
      id: string;
      ms: number;
      tokens: { input: number; cached: number; output: number; thinking: number };
      tools: string[];
      thinking: string;
      finish: string;
    }
  | { kind: "tool"; id: string; name: string; ms: number; args: string; result: string; ok: boolean };

export type DebugInfo = {
  runId: string;
  status: string;
  model: string;
  revision: number;
  started: string;
  ended: string;
  tokens: number;
  cost: number;
  steps: DebugStep[];
};

const TOOLS = ["shell", "browser_navigate", "browser_screenshot", "wait_for_input"];

const sampleSteps: DebugStep[] = [
  {
    kind: "model",
    id: "m1",
    ms: 1840,
    tokens: { input: 2310, cached: 0, output: 64, thinking: 212 },
    tools: TOOLS,
    thinking: "Austin wants to know if the site responds. Navigate to it first; a screenshot helps if it renders oddly.",
    finish: "STOP",
  },
  {
    kind: "tool",
    id: "m1-t1",
    name: "browser_navigate",
    ms: 3120,
    args: '{"url": "https://fernandflask.com"}',
    result: '{"url": "https://fernandflask.com/", "title": "Fern & Flask", "text": "Fern & Flask · Botanical cocktails…"}',
    ok: true,
  },
  {
    kind: "model",
    id: "m2",
    ms: 1210,
    tokens: { input: 2988, cached: 2310, output: 18, thinking: 96 },
    tools: TOOLS,
    thinking: "Page loaded with the right title. Grab a screenshot as proof.",
    finish: "STOP",
  },
  {
    kind: "tool",
    id: "m2-t1",
    name: "browser_screenshot",
    ms: 640,
    args: "{}",
    result: '{"artifact_id": "a-7f3e", "description": "PNG, 1280×800"}',
    ok: true,
  },
  {
    kind: "model",
    id: "m3",
    ms: 980,
    tokens: { input: 3102, cached: 2988, output: 41, thinking: 30 },
    tools: TOOLS,
    thinking: "Done. Answer in one line.",
    finish: "STOP",
  },
];

export function debugFor(message: Message, run: Run | undefined): DebugInfo {
  const steps = run?.status === "failed" ? sampleSteps.slice(0, 2).map((s) => (s.kind === "tool" ? { ...s, ok: false, result: '{"error": "exit 124: command timed out after 45 s"}' } : s)) : sampleSteps;
  const tokens = steps.reduce((sum, s) => (s.kind === "model" ? sum + s.tokens.input + s.tokens.output : sum), 0);
  return {
    runId: message.run_id,
    status: run?.status ?? "completed",
    model: "gemini-3.1-flash-lite",
    revision: run?.revision ?? 1,
    started: run?.started_at ?? message.created_at,
    ended: run?.finished_at ?? message.created_at,
    tokens,
    cost: tokens * 0.00000035,
    steps,
  };
}

/** A sample conversation for drafting without the runtime (`?demo`). */
export function demoSession(): Session {
  const at = (minutesAgo: number) => new Date(Date.now() - minutesAgo * 60_000).toISOString();
  return {
    paused: false,
    expires_at: at(-90),
    messages: [
      { id: "r1-user", run_id: "r1", role: "user", text: "hey", created_at: at(42) },
      { id: "r1-assistant", run_id: "r1", role: "assistant", text: "Hey. What do you need?", created_at: at(41) },
      { id: "r2-user", run_id: "r2", role: "user", text: "is the fern & flask site up?", created_at: at(9) },
      { id: "r2-assistant", run_id: "r2", role: "assistant", text: "It's up. The homepage loads in about 3 seconds.", created_at: at(8) },
      { id: "r3-user", run_id: "r3", role: "user", text: "find the receipt for the drill", created_at: at(3) },
      { id: "r3-assistant", run_id: "r3", role: "assistant", text: "I couldn't finish that. The search timed out.", created_at: at(2) },
    ],
    runs: [
      { id: "r1", status: "completed", cancel_requested: false, revision: 1, started_at: at(42), deadline: at(-90), finished_at: at(41), events: [], artifacts: [] },
      { id: "r2", status: "completed", cancel_requested: false, revision: 1, started_at: at(9), deadline: at(-90), finished_at: at(8), events: [], artifacts: [] },
      { id: "r3", status: "failed", cancel_requested: false, revision: 1, started_at: at(3), deadline: at(-90), finished_at: at(2), error: "tool shell failed", events: [], artifacts: [] },
    ],
  };
}
