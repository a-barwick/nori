// Paid: scenarios 110-116 on the deployed app, with the real models, the real clock, Austin's production calendar and
// the Routes API. Each step is one part of a scenario, because the overnight ones wait for real clock times.
//
// NORI_M11_RUNTIME_URL=<invoke root> NORI_M11_FIRESTORE=<documents URL> NORI_ENV_FILE=<root .env>
//   bun core/e2e/milestone11-live.js <step>
// Steps: 111 112 113 115 110-setup 110-check 110-accept 114-setup 114-check cleanup. Scenario 116 runs only locally: production has one real token to break.
//
// Events it creates are titled "Nori test" and deleted by the cleanup step. Memories it seeds are deleted by ID.
// Checks are on saved state: offers, tasks, delivery times and model calls, never exact wording.
import { spawnSync } from "node:child_process";
import { readFileSync, writeFileSync, existsSync } from "node:fs";

const base = process.env.NORI_M11_RUNTIME_URL?.replace(/\/+$/, "");
const documents = process.env.NORI_M11_FIRESTORE;
const owner = `${documents}/owners/austinbarwickdavis@gmail.com`;
const stateFile = process.env.NORI_M11_STATE ?? "/tmp/nori-m11-live.json";
const delay = (ms) => new Promise((done) => setTimeout(done, ms));
const saved = existsSync(stateFile) ? JSON.parse(readFileSync(stateFile, "utf8")) : { events: [], memories: [], results: {} };
const save = () => writeFileSync(stateFile, JSON.stringify(saved, null, 2));

// Credentials are read in memory from the existing root .env and never printed or copied.
const envFile = Object.fromEntries(readFileSync(process.env.NORI_ENV_FILE, "utf8").split("\n").map((line) => line.trim())
  .filter((line) => line && !line.startsWith("#") && line.includes("=")).map((line) => [line.slice(0, line.indexOf("=")), line.slice(line.indexOf("=") + 1).replace(/^["']|["']$/g, "")]));

let token = "", tokenAt = 0;
function headers(extra = {}) {
  if (Date.now() - tokenAt > 20 * 60_000) {
    const read = spawnSync("gcloud", ["auth", "print-access-token"], { encoding: "utf8" });
    if (read.status !== 0) throw new Error("gcloud token unavailable");
    token = read.stdout.trim();
    tokenAt = Date.now();
  }
  return { Authorization: `Bearer ${token}`, ...extra };
}
async function send(url, options) {
  for (let attempt = 0; ; attempt++) {
    const response = await fetch(url, { ...options, signal: AbortSignal.timeout(120_000) });
    // Agent Runtime limits requests per minute and refuses the rest before they reach the service.
    if (response.status !== 429 || attempt >= 12) return response;
    await delay(10_000);
  }
}
async function post(operation, value = {}) {
  const response = await send(`${base}/api/${operation}`, { method: "POST", headers: headers({ "Content-Type": "application/json" }), body: JSON.stringify(value) });
  return { status: response.status, body: await response.json().catch(() => ({})) };
}
const snapshot = async () => (await send(`${base}/v1/snapshot`, { headers: headers() })).json();
async function until(read, accept, label, timeout = 420_000) {
  const deadline = Date.now() + timeout;
  let last;
  while (Date.now() < deadline) {
    try {
      last = await read();
      if (accept(last)) return last;
    } catch (error) { last = String(error); }
    await delay(5000);
  }
  throw new Error(`Timed out waiting for ${label}: ${JSON.stringify(last)?.slice(0, 1500)}`);
}
const quiet = (now) => !now.conversation.pending && !now.conversation.processing;

function decode(value) {
  if ("arrayValue" in value) return (value.arrayValue.values ?? []).map(decode);
  if ("mapValue" in value) return Object.fromEntries(Object.entries(value.mapValue.fields ?? {}).map(([key, item]) => [key, decode(item)]));
  return value.stringValue ?? value.integerValue ?? value.timestampValue ?? value.booleanValue ?? value.doubleValue ?? null;
}
async function collection(name) {
  const data = await (await send(`${owner}/${name}?pageSize=300`, { headers: headers() })).json();
  return (data.documents ?? []).map((doc) => decode({ mapValue: doc }));
}
async function offer(id) {
  const response = await send(`${owner}/proactive_offers/${id}`, { headers: headers() });
  return response.ok ? decode({ mapValue: await response.json() }) : null;
}
async function usageFor(offerID) {
  return (await collection("model_usage")).filter((u) => u.input_id === `input-${offerID}`);
}

// The operator's marker: the outer model sees nothing from before it, and nothing is deleted.
async function freshContext() {
  await until(snapshot, quiet, "the conversation to be idle");
  const at = new Date().toISOString();
  const response = await send(`${owner}/conversation/control?updateMask.fieldPaths=context_from`, {
    method: "PATCH", headers: headers({ "Content-Type": "application/json" }), body: JSON.stringify({ fields: { context_from: { timestampValue: at } } }),
  });
  if (!response.ok) throw new Error(`fresh context: HTTP ${response.status}`);
  await delay(1100);
}
async function say(text) {
  const id = crypto.randomUUID();
  const sent = await post("turn", { id, message: text });
  if (!sent.status.toString().startsWith("2")) throw new Error(`turn: HTTP ${sent.status}`);
  await delay(2000);
  const now = await until(snapshot, (s) => quiet(s) && s.messages.some((m) => m.id === `${id}-user` && m.turn_id), `"${text}" to settle`);
  const turn = now.messages.find((m) => m.id === `${id}-user`).turn_id;
  return { id, now, reply: now.messages.findLast((m) => m.role === "assistant" && m.turn_id === turn) };
}
async function seedMemory(text) {
  const id = crypto.randomUUID();
  const at = new Date().toISOString();
  const fields = { id: { stringValue: id }, text: { stringValue: text }, about: { stringValue: "Austin" }, source_input_id: { stringValue: "scenario-seed" },
    source_text: { stringValue: text }, created_at: { timestampValue: at }, updated_at: { timestampValue: at } };
  const response = await send(`${owner}/memories?documentId=${id}`, { method: "POST", headers: headers({ "Content-Type": "application/json" }), body: JSON.stringify({ fields }) });
  if (!response.ok) throw new Error(`seed memory: HTTP ${response.status}`);
  saved.memories.push(id);
  save();
  return id;
}
async function check(manual = true) {
  const result = await post("proactive", { manual });
  if (result.status !== 200) throw new Error(`check: HTTP ${result.status}: ${JSON.stringify(result.body)}`);
  return result.body;
}

// Google Calendar, as Austin's connected account.
let calendarToken = "";
async function calendar(method, path, body) {
  if (!calendarToken) {
    const response = await fetch("https://oauth2.googleapis.com/token", { method: "POST", headers: { "Content-Type": "application/x-www-form-urlencoded" },
      body: new URLSearchParams({ client_id: envFile.GOOGLE_CALENDAR_CLIENT_ID, client_secret: envFile.GOOGLE_CALENDAR_CLIENT_SECRET, refresh_token: envFile.GOOGLE_CALENDAR_REFRESH_TOKEN, grant_type: "refresh_token" }) });
    if (!response.ok) throw new Error(`calendar token: HTTP ${response.status}`);
    calendarToken = (await response.json()).access_token;
  }
  const target = encodeURIComponent(envFile.GOOGLE_CALENDAR_WRITE_ID || "primary");
  const response = await fetch(`https://www.googleapis.com/calendar/v3/calendars/${target}${path}`, { method, headers: { Authorization: `Bearer ${calendarToken}`, "Content-Type": "application/json" }, body: body && JSON.stringify(body) });
  if (!response.ok) throw new Error(`calendar ${method} ${path}: HTTP ${response.status}`);
  return response.status === 204 ? {} : response.json();
}
async function event(title, location, start, minutes) {
  const created = await calendar("POST", "/events", {
    summary: `Nori test: ${title}`, location, start: { dateTime: new Date(start).toISOString() }, end: { dateTime: new Date(+start + minutes * 60_000).toISOString() },
    reminders: { useDefault: false, overrides: [] },
  });
  saved.events.push(created.id);
  save();
  return created;
}
const oakland = "3300 Grand Ave, Oakland, CA 94610";
const sanFrancisco = "1 Ferry Building, San Francisco, CA 94111";
const minutesFromNow = (minutes) => new Date(Math.ceil((Date.now() + minutes * 60_000) / 300_000) * 300_000);
// Local wall-clock time in America/Los_Angeles on the day after the given date.
function laTime(dayOffset, hour, minute) {
  const now = new Date();
  const parts = Object.fromEntries(new Intl.DateTimeFormat("en-US", { timeZone: "America/Los_Angeles", year: "numeric", month: "2-digit", day: "2-digit" }).formatToParts(now).map((p) => [p.type, p.value]));
  const guess = new Date(Date.UTC(+parts.year, +parts.month - 1, +parts.day + dayOffset, hour, minute));
  const offset = new Date(guess.toLocaleString("en-US", { timeZone: "America/Los_Angeles" })) - new Date(guess.toLocaleString("en-US", { timeZone: "UTC" }));
  return new Date(+guess - offset);
}
const laClock = (at) => new Date(at).toLocaleTimeString("en-US", { timeZone: "America/Los_Angeles", hour: "numeric", minute: "2-digit" });

function result(id, pass, detail) {
  saved.results[id] = { pass, detail, at: new Date().toISOString() };
  save();
  console.log(JSON.stringify({ scenario: id, pass, detail }, null, 2));
  if (!pass) process.exitCode = 1;
}
const offerMessage = (now, id) => now.messages.find((m) => m.id === `turn-${id}-assistant`);
async function delivered(id, timeout = 600_000) {
  const done = await until(() => offer(id), (o) => o && o.status !== "planned" && o.status !== "planning", `offer ${id} to be delivered`, timeout);
  return { offer: done, now: await snapshot() };
}
// A check returns every offer in its window, including earlier scenarios'; each step takes only its own.
async function own(found, title) {
  const offers = await Promise.all(found.offers.map(offer));
  return offers.filter((o) => o && o.finding.first.title === `Nori test: ${title}`).map((o) => o.id);
}
const otherTasks = (now, since) => now.runs.filter((run) => Date.parse(run.started_at) >= since && !run.offer);

const steps = {
  // Two events at one address and one with none: no offer, no model call. Run through the Scheduler job's own slot.
  async 111() {
    const start = minutesFromNow(240);
    await event("Standup", oakland, start, 30);
    await event("Review", oakland, +start + 35 * 60_000, 25);
    await event("Lunch", "", +start + 65 * 60_000, 30);
    const before = (await collection("model_usage")).length;
    const offersBefore = new Set((await collection("proactive_offers")).map((o) => o.id));
    const run = spawnSync("gcloud", ["--project", "bot-111549", "scheduler", "jobs", "run", "nori-proactive-check", "--location=us-central1"], { encoding: "utf8" });
    if (run.status !== 0) throw new Error(`scheduler run: ${run.stderr}`);
    const checks = await until(() => collection("proactive_checks"), (list) => list.some((c) => c.id.startsWith("check-") && c.status !== "running"), "the scheduled check", 300_000);
    const latest = checks.filter((c) => c.id.startsWith("check-")).sort((a, b) => Date.parse(b.started_at) - Date.parse(a.started_at))[0];
    const fresh = (await collection("proactive_offers")).filter((o) => !offersBefore.has(o.id));
    const testOffers = fresh.filter((o) => o.finding.first.title.startsWith("Nori test") || o.finding.second.title.startsWith("Nori test"));
    const calls = (await collection("model_usage")).length - before;
    result("111", latest.status === "done" && testOffers.length === 0, { check: latest, test_offers: testOffers.length, other_new_offers: fresh.length - testOffers.length, model_calls: calls });
  },

  // An offer is delivered, Austin turns it down in words, and the next check finds the same conflict without repeating.
  async 112() {
    await freshContext();
    const start = minutesFromNow(60);
    await event("Dentist", oakland, start, 30);
    await event("Team meeting", sanFrancisco, +start + 40 * 60_000, 30);
    const first = await check();
    const [id] = await own(first, "Dentist");
    if (!id) return result("112", false, { reason: "no offer", first });
    const { offer: shown, now } = await delivered(id);
    const message = offerMessage(now, id);
    const answer = await say("no thanks, I'll leave it");
    const tasks = otherTasks(answer.now, Date.parse(message.created_at));
    const second = await check();
    await delay(20_000);
    const after = await snapshot();
    const copies = after.messages.filter((m) => m.parts?.some((p) => p.type === "offer" && p.id === id)).length;
    result("112", shown.status === "delivered" && second.offers.includes(id) && copies === 1 && tasks.length === 0, {
      offer: id, status: shown.status, repeat_check_offers: second.offers, copies, tasks_started: tasks.length, reply: answer.reply?.text,
    });
  },

  // The offer comes due while a reply is in progress; the reply finishes and the offer appears once, on its own.
  async 113() {
    await freshContext();
    const start = minutesFromNow(60);
    await event("Gym class", oakland, start, 45);
    await event("Coffee with Sam", sanFrancisco, +start + 55 * 60_000, 30);
    const turnID = crypto.randomUUID();
    const sent = await post("turn", { id: turnID, message: "what's the weather saturday?" });
    if (!sent.status.toString().startsWith("2")) throw new Error(`turn: HTTP ${sent.status}`);
    const found = await check();
    const [id] = await own(found, "Gym class");
    if (!id) return result("113", false, { reason: "no offer", found });
    await delivered(id);
    const now = await until(snapshot, quiet, "the conversation to settle");
    const turn = now.messages.find((m) => m.id === `${turnID}-user`)?.turn_id;
    const replies = now.messages.filter((m) => m.role === "assistant" && m.turn_id === turn);
    const message = offerMessage(now, id);
    const copies = now.messages.filter((m) => m.parts?.some((p) => p.type === "offer" && p.id === id)).length;
    // Its own message: not the reply's turn, and not inside the reply's text.
    const separate = message && message.turn_id !== turn && !replies.some((r) => r.parts?.some((p) => p.type === "offer"));
    result("113", !!message && copies === 1 && separate && replies.length >= 1, {
      offer: id, offer_at: message?.created_at, replies: replies.map((r) => ({ at: r.created_at, text: r.text.slice(0, 200) })), copies,
    });
  },

  // Five conflicts in one day: at most three offers that day, and nothing says the rest were fine.
  async 115() {
    await freshContext();
    // Late afternoon, clear of the other scenarios' events, which would change which event follows which.
    const start = new Date(Math.max(+minutesFromNow(90), +laTime(0, 15, 30)));
    for (let i = 0; i < 6; i++) await event(`Errand ${i + 1}`, i % 2 ? sanFrancisco : oakland, +start + i * 50 * 60_000, 40);
    const found = await check();
    const all = await Promise.all(found.offers.map(offer));
    const ids = new Set(all.filter((o) => o?.finding.first.title.startsWith("Nori test: Errand")).map((o) => o.id));
    const list = await until(async () => (await collection("proactive_offers")).filter((o) => ids.has(o.id)), (l) => l.length > 0 && l.every((o) => o.status !== "planned" && o.status !== "planning"), "the offers to settle", 900_000);
    const day = new Intl.DateTimeFormat("en-CA", { timeZone: "America/Los_Angeles" }).format(new Date());
    const today = (await collection("proactive_offers")).filter((o) => o.delivered_at && new Intl.DateTimeFormat("en-CA", { timeZone: "America/Los_Angeles" }).format(new Date(o.delivered_at)) === day);
    const now = await snapshot();
    const texts = now.messages.filter((m) => m.role === "assistant" && Date.parse(m.created_at) > Date.now() - 3_600_000).map((m) => m.text.toLowerCase());
    const claimsFine = texts.some((t) => /no other conflicts|everything else (looks|is) (fine|clear)|the rest (look|are) fine/.test(t));
    result("115", ids.size >= 5 && today.length <= 3 && !claimsFine, {
      found: found.found, offers: list.map((o) => ({ status: o.status, skip: o.skip_reason })), delivered_today: today.length,
    });
  },

  // Set up before midnight: a dentist visit tomorrow that leaves too little time to reach a meeting across the bay.
  async "110-setup"() {
    const end = laTime(1, 13, 45);
    await event("Dentist appointment", oakland, +end - 45 * 60_000, 45);
    await event("Planning meeting", sanFrancisco, laTime(1, 14, 0), 60);
    console.log(JSON.stringify({ dentist_ends: laClock(end), meeting: laClock(laTime(1, 14, 0)) }));
  },
  // After the midnight run: one offer, shown at 8 am, with no task started that changes anything.
  async "110-check"() {
    const offers = (await collection("proactive_offers")).filter((o) => o.finding.first.title === "Nori test: Dentist appointment");
    const shown = offers[0];
    if (!shown) return result("110", false, { reason: "no offer for the dentist conflict" });
    const { offer: done, now } = await delivered(shown.id, 10 * 3_600_000);
    const message = offerMessage(now, shown.id);
    const local = laClock(done.delivered_at);
    const text = message?.text ?? "";
    const names = /dentist/i.test(text) && /meeting/i.test(text);
    const numbers = text.includes(String(done.finding.drive_minutes)) && text.includes(String(done.finding.gap_minutes));
    const writes = now.runs.filter((run) => Date.parse(run.started_at) >= Date.parse(done.created_at) && !run.offer);
    saved.offer110 = shown.id;
    save();
    result("110-offer", offers.length === 1 && done.status === "delivered" && done.check_id.startsWith("check-") && /^8:0\d AM$/.test(local) && names && numbers && writes.length === 0, {
      offer: shown.id, check: done.check_id, delivered_local: local, timing: done.timing, prep: done.prep_run_id, names, numbers, tasks_before_yes: writes.length, text,
    });
  },
  // Austin says yes: a task starts on the normal path and nothing on the calendar changes without approval.
  async "110-accept"() {
    await freshContext();
    const id = saved.offer110;
    const before = Date.now();
    const answer = await say("yes, find me another time");
    const tasks = otherTasks(answer.now, before);
    await delay(60_000);
    const later = await snapshot();
    const run = later.runs.find((r) => r.id === tasks[0]?.id);
    // A write is recorded as an operation before it's sent. Anything past prepared means it was sent without approval.
    const operations = [];
    for (const task of tasks) operations.push(...await collection(`runs/${task.id}/operations`));
    const confirmed = operations.filter((op) => ["reversible_write", "sensitive_send"].includes(op.effect_class) && op.outcome !== "prepared" && op.outcome !== "needs_input");
    result("110-accept", tasks.length >= 1 && confirmed.length === 0, {
      offer: id, tasks: tasks.map((t) => ({ id: t.id, status: t.status, read_only: t.read_only })), question: run?.question?.kind, writes: confirmed.length, reply: answer.reply?.text?.slice(0, 300),
    });
  },

  // After midnight: a memory says Austin is up by 6, and an overnight check finds a conflict.
  async "114-setup"() {
    await seedMemory("Austin is usually up by 6am.");
    await event("Physio", oakland, laTime(0, 11, 0), 45);
    await event("Lunch meeting", sanFrancisco, laTime(0, 12, 0), 60);
    const found = await check();
    [saved.offer114] = await own(found, "Physio");
    save();
    console.log(JSON.stringify(found));
  },
  async "114-check"() {
    const id = saved.offer114;
    if (!id) return result("114", false, { reason: "no offer" });
    const { offer: done } = await delivered(id, 10 * 3_600_000);
    const hour = new Date(done.deliver_at).toLocaleString("en-US", { timeZone: "America/Los_Angeles", hour: "numeric", hour12: false });
    const plans = await usageFor(id);
    result("114", done.status === "delivered" && +hour >= 5 && +hour < 7 && done.timing.startsWith("model:") && plans.length >= 1, {
      offer: id, deliver_local: laClock(done.deliver_at), timing: done.timing, planning_calls: plans.length,
    });
    for (const memory of saved.memories) await send(`${owner}/memories/${memory}`, { method: "DELETE", headers: headers() });
    saved.memories = [];
    save();
  },

  async cleanup() {
    for (const id of saved.events) await calendar("DELETE", `/events/${id}`).catch((error) => console.log(String(error)));
    for (const memory of saved.memories) await send(`${owner}/memories/${memory}`, { method: "DELETE", headers: headers() });
    console.log(JSON.stringify({ deleted_events: saved.events.length, deleted_memories: saved.memories.length }));
    saved.events = [];
    saved.memories = [];
    save();
  },
};

const step = process.argv[2];
if (!steps[step]) throw new Error(`Unknown step ${step}. Steps: ${Object.keys(steps).join(" ")}`);
await steps[step]();
