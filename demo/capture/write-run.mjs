// Writes capture/run.json: the scripted snapshots for the film, in the runtime's
// session format plus the film `frame` each one lands on. Copy is from SCRIPT.md.
//   node capture/write-run.mjs
import { writeFileSync } from "node:fs";
import { resolve } from "node:path";

const EVENING = Date.parse("2027-02-17T01:30:00Z"); // 5:30 PM in Los Angeles
const MORNING = Date.parse("2027-02-17T16:00:00Z"); // 8:00 AM the next day
const at = (frame) => new Date(frame < 1455 ? EVENING + (frame - 540) * 1000 : MORNING + (frame - 1615) * 1000).toISOString();

const say = (id, role, text, frame) => ({ id: `${id}-${role}`, run_id: "", role, text, created_at: at(frame), turn_id: id });
const task = (id, request, frame, end) => ({
  id, request, status: end ? "completed" : "running", cancel_requested: false, budget_usd: 1, started_at: at(frame), finished_at: end ? at(end) : undefined,
  events: [{ created_at: at(frame), kind: "model", text: "Thinking" }], artifacts: null,
});

const id = (n) => `00000000-0000-4000-8000-${String(n).padStart(12, "0")}`;
const m = {
  grab: say(id(1), "user", "Grab numbers for the Acme account", 585),
  onIt: say(id(2), "assistant", "On it!", 600),
  dinner: say(id(3), "user", "Also find a dinner spot near their office", 660),
  weather: say(id(4), "user", "Also what's the weather there?", 690),
  both: say(id(5), "assistant", "Sure, adding both.", 750),
  chicago: say(id(6), "assistant", "It's looking chilly in Chicago tonight, 48° and windy. Bring a coat! 🧥", 825),
  kaze: say(id(7), "assistant", "Found a spot! Kaze is two blocks from their office.", 975),
  acme: say(id(8), "assistant", "By the way, got those Acme numbers:\n\n- $1.2M a year, up 18%\n- 340 of 400 seats active\n- Renews March 14", 1140),
  morning: say(id(9), "assistant", "Morning! Hope dinner went well. 🍣 Priya asked about multi-year pricing twice, so I drafted her a proposal.", 1615),
  app: say(id(10), "user", "Amazing. Turn those numbers into an app for Sam.", 2115),
};

const numbers = (end) => task("10000000-0000-4000-8000-000000000001", "Pull Acme numbers", 600, end);
const dinner = (end) => task("10000000-0000-4000-8000-000000000002", "Find dinner near Acme", 750, end);
const weather = (end) => task("10000000-0000-4000-8000-000000000003", "Check Chicago weather", 750, end);
const evening = [m.grab, m.onIt, m.dinner, m.weather, m.both, m.chicago, m.kaze, m.acme];
const done = [numbers(1140), dinner(975), weather(825)];

const steps = [
  { frame: 540, messages: [], runs: [] },
  { frame: 585, messages: evening.slice(0, 1), runs: [], thinking: true },
  { frame: 600, messages: evening.slice(0, 2), runs: [numbers()] },
  { frame: 660, messages: evening.slice(0, 3), runs: [numbers()], thinking: true },
  { frame: 690, messages: evening.slice(0, 4), runs: [numbers()], thinking: true },
  { frame: 750, messages: evening.slice(0, 5), runs: [numbers(), dinner(), weather()] },
  { frame: 825, messages: evening.slice(0, 6), runs: [numbers(), dinner(), weather(825)] },
  { frame: 975, messages: evening.slice(0, 7), runs: [numbers(), dinner(975), weather(825)] },
  { frame: 1140, messages: evening, runs: done },
  { frame: 1615, messages: [...evening, m.morning], runs: done },
  { frame: 2115, messages: [...evening, m.morning, m.app], runs: done, thinking: true },
];

const conversation = (revision, thinking) => ({ consumed: revision, frozen: false, input_ids: [], introducing: false, pending: false, processing: !!thinking, restart_count: 0, revision, routing_blocked: false });
const run = steps.map(({ frame, messages, runs, thinking }, i) => ({
  frame, ts: Date.parse(at(frame)),
  session: { paused: false, messages, runs, dispatches: [], conversation: conversation(i + 1, thinking) },
}));
writeFileSync(resolve(import.meta.dirname, "run.json"), JSON.stringify(run));
console.log("run.json", run.length, "snapshots");
