// Writes capture/run.json: the scripted snapshots for the film, in the runtime's
// session format plus the film `frame` each one lands on. Copy is from SCRIPT.md.
//   node capture/write-run.mjs
import { writeFileSync } from "node:fs";
import { resolve } from "node:path";

const EVENING = Date.parse("2027-02-17T01:30:00Z"); // 5:30 PM in Los Angeles
const MORNING = Date.parse("2027-02-17T16:00:00Z"); // 8:00 AM the next day
const at = (frame) => new Date(frame < 1590 ? EVENING + (frame - 480) * 1000 : MORNING + (frame - 1750) * 1000).toISOString();

const say = (id, role, text, frame) => ({ id: `${id}-${role}`, run_id: "", role, text, created_at: at(frame), turn_id: id });
const task = (id, request, frame, end) => ({
  id, request, status: end ? "completed" : "running", cancel_requested: false, budget_usd: 1, started_at: at(frame), finished_at: end ? at(end) : undefined,
  events: [{ created_at: at(frame), kind: "model", text: "Thinking" }], artifacts: null,
});

const id = (n) => `00000000-0000-4000-8000-${String(n).padStart(12, "0")}`;
const m = {
  grab: say(id(1), "user", "Grab numbers for the Acme account", 525),
  dinner: say(id(2), "user", "Also find a dinner spot near their office", 570),
  weather: say(id(3), "user", "Also what's the weather there?", 690),
  onIt: say(id(4), "assistant", "On it!", 780),
  chicago: say(id(5), "assistant", "48° and windy in Chicago tonight. Bring a coat! 🧥", 885),
  kaze: say(id(6), "assistant", "Kaze has a table for 6 at 7PM, two blocks from their office.", 1020),
  acme: say(id(7), "assistant", "- $1.2M a year, up 18%\n- 340 of 400 seats active\n- Renews March 14", 1275),
  morning: say(id(8), "assistant", "Morning! Hope dinner went well. 🍣", 1750),
  priya: say(id(9), "assistant", "Priya asked about multi-year pricing on two of your last three calls.", 1770),
  ready: say(id(10), "assistant", "Proposal's ready.", 1950),
  app: say(id(11), "user", "Amazing. Turn those numbers into an app for Sam.", 2445),
};

const dinnerTask = (end) => task("10000000-0000-4000-8000-000000000001", "Find dinner near Acme", 780, end);
const numbersTask = (end) => task("10000000-0000-4000-8000-000000000002", "Pull Acme numbers", 781, end);
const done = [numbersTask(1275), dinnerTask(1020)];
const evening = [m.grab, m.dinner, m.weather, m.onIt, m.chicago, m.kaze, m.acme];

const steps = [
  { frame: 480, messages: [], runs: [] },
  { frame: 525, messages: evening.slice(0, 1), runs: [] },
  { frame: 570, messages: evening.slice(0, 2), runs: [] },
  { frame: 690, messages: evening.slice(0, 3), runs: [] },
  { frame: 780, messages: evening.slice(0, 4), runs: [numbersTask(), dinnerTask()] },
  { frame: 885, messages: evening.slice(0, 5), runs: [numbersTask(), dinnerTask()] },
  { frame: 1020, messages: evening.slice(0, 6), runs: [numbersTask(), dinnerTask(1020)] },
  { frame: 1275, messages: evening, runs: done },
  { frame: 1750, messages: [...evening, m.morning], runs: done },
  { frame: 1770, messages: [...evening, m.morning, m.priya], runs: done },
  { frame: 1950, messages: [...evening, m.morning, m.priya, m.ready], runs: done },
  { frame: 2445, messages: [...evening, m.morning, m.priya, m.ready, m.app], runs: done },
];

const conversation = (revision) => ({ consumed: revision, frozen: false, input_ids: [], introducing: false, pending: false, processing: false, restart_count: 0, revision, routing_blocked: false });
const run = steps.map(({ frame, messages, runs }, i) => ({
  frame, ts: Date.parse(at(frame)),
  session: { paused: false, messages, runs, dispatches: [], conversation: conversation(i + 1) },
}));
writeFileSync(resolve(import.meta.dirname, "run.json"), JSON.stringify(run));
console.log("run.json", run.length, "snapshots");
