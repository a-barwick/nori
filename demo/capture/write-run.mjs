// Writes capture/run.json: the scripted snapshots for film v3, in the runtime's
// session format plus the film `frame` each one lands on. Copy is from SCRIPT.md.
//   node capture/write-run.mjs
import { writeFileSync } from "node:fs";
import { resolve } from "node:path";

const EVENING = Date.parse("2027-02-17T01:30:00Z"); // 5:30 PM in Los Angeles
const MORNING = Date.parse("2027-02-17T16:00:00Z"); // 8:00 AM the next day
const at = (frame) => new Date(frame < 930 ? EVENING + (frame - 300) * 1000 : MORNING + (frame - 990) * 1000).toISOString();

const say = (id, role, text, frame) => ({ id: `${id}-${role}`, run_id: "", role, text, created_at: at(frame), turn_id: id });
const task = (id, request, frame, end) => ({
  id, request, status: end ? "completed" : "running", cancel_requested: false, budget_usd: 1, started_at: at(frame), finished_at: end ? at(end) : undefined,
  events: [{ created_at: at(frame), kind: "model", text: "Thinking" }], artifacts: null,
});

const morning = [
  "Morning! Hope dinner with Acme went well. 🍣",
  "\n\nWhile you were out:",
  "\n- Pulled the three things they raised on your last calls",
  "\n- Drafted the renewal proposal around them",
  "\n- Moved your 9:00 so you have time to read it",
  "\n\nReady to send when you are.",
];
const lineFrames = [990, 1005, 1020, 1035, 1050, 1080];

const m = {
  grab: say("00000000-0000-4000-8000-000000000001", "user", "Grab numbers for the Acme account", 345),
  dinner: say("00000000-0000-4000-8000-000000000002", "user", "Also find a good spot for dinner near their office", 375),
  weather: say("00000000-0000-4000-8000-000000000003", "user", "Also what's the weather like over there?", 405),
  onIt: say("00000000-0000-4000-8000-000000000004", "assistant", "Not a problem! On it. I'll make that invite big enough for the whole team.", 435),
  chicago: say("00000000-0000-4000-8000-000000000005", "assistant", "Chicago: 48° and windy tonight. Bring a coat.", 480),
  kaze: say("00000000-0000-4000-8000-000000000006", "assistant", "Table for six at Kaze, 7:00 PM, two blocks from their office. There's a vegan menu. The invite's on everyone's calendar.", 630),
  // Hard line breaks hold the same wrap on desktop and phone, so scene 5 can hold it still.
  acme: say("00000000-0000-4000-8000-000000000007", "assistant", "Acme is at $1.2M a year, up 18%.  \n340 of 400 seats are active.  \nRenewal is March 14.", 720),
  amazing: say("00000000-0000-4000-8000-000000000009", "user", "Amazing. Now turn the Acme numbers into a dashboard and share it with Sam.", 1230),
};
const morningId = "00000000-0000-4000-8000-000000000008";

const dinnerTask = (end) => task("10000000-0000-4000-8000-000000000001", "Find dinner near Acme", 435, end);
const numbersTask = (end) => task("10000000-0000-4000-8000-000000000002", "Pull Acme numbers", 436, end);
const evening = [m.grab, m.dinner, m.weather, m.onIt, m.chicago, m.kaze, m.acme];

const steps = [
  { frame: 300, messages: [], runs: [] },
  { frame: 345, messages: [m.grab], runs: [] },
  { frame: 375, messages: evening.slice(0, 2), runs: [] },
  { frame: 405, messages: evening.slice(0, 3), runs: [] },
  { frame: 435, messages: evening.slice(0, 4), runs: [numbersTask(), dinnerTask()] },
  { frame: 480, messages: evening.slice(0, 5), runs: [numbersTask(), dinnerTask()] },
  { frame: 630, messages: evening.slice(0, 6), runs: [numbersTask(), dinnerTask(630)] },
  { frame: 720, messages: evening, runs: [numbersTask(720), dinnerTask(630)] },
  ...lineFrames.map((frame, i) => ({ frame, messages: [...evening, say(morningId, "assistant", morning.slice(0, i + 1).join(""), 990)], runs: [numbersTask(720), dinnerTask(630)] })),
  { frame: 1230, messages: [...evening, say(morningId, "assistant", morning.join(""), 990), m.amazing], runs: [numbersTask(720), dinnerTask(630)] },
];

const conversation = (revision) => ({ consumed: revision, frozen: false, input_ids: [], introducing: false, pending: false, processing: false, restart_count: 0, revision, routing_blocked: false });
const run = steps.map(({ frame, messages, runs }, i) => ({
  frame, ts: Date.parse(at(frame)),
  session: { paused: false, messages, runs, dispatches: [], conversation: conversation(i + 1) },
}));
writeFileSync(resolve(import.meta.dirname, "run.json"), JSON.stringify(run));
console.log("run.json", run.length, "snapshots");
