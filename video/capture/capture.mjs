// Films the real Nori frontend (web/, on port 3211) with a scripted session and
// real public Docusign data. Nothing here touches the runtime: /api/* is answered
// in the browser. Outputs public/clips/{desktop,phone}.mp4 and markers.json.
//   node capture/capture.mjs [desktop|phone]
import { chromium } from "playwright-core";
import { spawnSync } from "node:child_process";
import { mkdirSync, readFileSync, rmSync, writeFileSync } from "node:fs";
import { join, resolve } from "node:path";
import { tmpdir } from "node:os";

const ORIGIN = "http://127.0.0.1:3211";
const root = resolve(import.meta.dirname, "..");
const iso = (ms) => new Date(ms).toISOString();
const sleep = (ms) => new Promise((r) => setTimeout(r, ms));

const BRIEF = `# Docusign Q2 FY27 brief

Reported September 3, 2026, for the quarter ended July 31.

## The numbers

- Revenue: **$875.7M**, up 9% year over year
- Non-GAAP EPS: **$1.16**, up from $0.92
- Free cash flow: **$295.8M**, a 34% margin
- IAM: **15.1%** of total ARR, up from 12.6% last quarter

## Full-year guidance, raised

- Revenue: **$3,499M to $3,507M**
- ARR growth: **8.5% to 9.0%**
- IAM exiting Q4: about **18% to 19%** of ARR

## Quote to reuse

"Our AI agents are now securely executing contract workflows end-to-end." Allan Thygesen, CEO

Source: Docusign Q2 FY27 earnings release, investor.docusign.com`;

const TASK_A = "Pull Docusign's Q2 results from the investor site and write me a brief.";
const TASK_B = "Find the latest Docusign and Salesforce news.";

class Backend {
  session;
  revision = 1;
  constructor() {
    const y = Date.now() - 20 * 3600e3;
    this.session = {
      paused: false, runs: [], dispatches: [], traces: [], usage: [],
      messages: [
        { id: "seed-1", run_id: "", role: "user", text: "Keep my briefs under 200 words, numbers first.", created_at: iso(y) },
        { id: "seed-2", run_id: "", role: "assistant", text: "Got it. Briefs stay under 200 words, numbers first.", created_at: iso(y + 4000) },
      ],
      conversation: this.conversation(false),
    };
  }
  conversation(processing) {
    return { pending: false, processing, input_ids: this.session?.conversation?.input_ids ?? [], restart_count: 0, frozen: false, revision: ++this.revision, consumed: 0 };
  }
  touch(processing = false) { this.session.conversation = this.conversation(processing); }
  user(id, text) {
    this.session.messages.push({ id: `${id}-user`, run_id: "", role: "user", text, created_at: iso(Date.now()) });
    this.touch(true);
  }
  reply(text) {
    this.session.messages.push({ id: crypto.randomUUID(), run_id: "", role: "assistant", text, created_at: iso(Date.now()) });
    this.touch(false);
  }
  startRun(id, request) {
    this.session.runs.unshift({ id, request, status: "running", cancel_requested: false, temporal: true, protocol_version: 2, position: 1, event_sequence: 0, revision: 1, incorporated: 1, budget_usd: 1, started_at: iso(Date.now()), events: [], artifacts: [] });
    this.session.dispatches.push({ id, request, status: "accepted", created_at: iso(Date.now()) });
    this.touch();
  }
  run(id) { return this.session.runs.find((r) => r.id === id); }
  event(id, kind, text) {
    const r = this.run(id);
    r.event_sequence += 1;
    r.events.push({ id: `${id}-${r.event_sequence}`, sequence: r.event_sequence, instruction_revision: 1, kind, text, created_at: iso(Date.now()) });
    this.touch();
  }
  artifact(id, name) {
    const r = this.run(id);
    r.event_sequence += 1;
    r.artifacts.push({ id: name, kind: "screenshot" });
    this.touch();
  }
  finish(id, result) {
    const r = this.run(id);
    Object.assign(r, { status: "completed", result, finished_at: iso(Date.now()) });
    r.event_sequence += 1;
    this.touch();
  }
}

async function mount(page, backend) {
  await page.route("**/api/**", async (route) => {
    const url = new URL(route.request().url());
    const op = url.pathname.split("/").pop();
    const body = route.request().postDataJSON() ?? {};
    if (op === "artifact") {
      return route.fulfill({ status: 200, contentType: "image/png", body: readFileSync(join(root, "public/real", `${body.artifact_id}.png`)) });
    }
    if (op === "turn") {
      backend.user(body.id, body.message);
      backend.onTurn?.(body.message);
    }
    return route.fulfill({ status: 200, contentType: "application/json", body: JSON.stringify(backend.session) });
  });
  // A visible pointer: headless screencasts do not draw one.
  await page.addInitScript(() => {
    addEventListener("DOMContentLoaded", () => {
      const style = document.createElement("style");
      style.textContent = "[role=tooltip],[data-radix-popper-content-wrapper]{display:none!important}" + (innerWidth > 2000 ? "html{zoom:2}" : "");
      document.documentElement.append(style);
      if (matchMedia("(pointer: coarse)").matches) return;
      const c = document.createElement("div");
      c.id = "film-cursor";
      c.innerHTML = '<svg width="26" height="26" viewBox="0 0 24 24"><path d="M5 3l14 8-6.5 1.6L9.5 19z" fill="#120a2b" stroke="#fff" stroke-width="1.6" stroke-linejoin="round"/></svg>';
      c.style.cssText = "position:fixed;left:-40px;top:-40px;z-index:2147483647;pointer-events:none;transform-origin:5px 3px;transition:transform .1s;";
      document.documentElement.append(c);
      addEventListener("mousemove", (e) => { const z = innerWidth > 2000 ? 2 : 1; c.style.left = e.clientX / z - 5 + "px"; c.style.top = e.clientY / z - 3 + "px"; }, true);
      addEventListener("mousedown", () => { c.style.transform = "scale(.82)"; }, true);
      addEventListener("mouseup", () => { c.style.transform = ""; }, true);
    });
  });
}

class Film {
  constructor(page, name) { this.page = page; this.name = name; this.frames = []; this.markers = {}; this.dir = join(tmpdir(), `nori-film-${name}`); }
  async start() {
    rmSync(this.dir, { recursive: true, force: true });
    mkdirSync(this.dir, { recursive: true });
    this.cdp = await this.page.context().newCDPSession(this.page);
    this.cdp.on("Page.screencastFrame", (f) => {
      const file = join(this.dir, `${String(this.frames.length).padStart(6, "0")}.jpg`);
      writeFileSync(file, Buffer.from(f.data, "base64"));
      this.frames.push({ file, t: f.metadata.timestamp });
      this.cdp.send("Page.screencastFrameAck", { sessionId: f.sessionId }).catch(() => {});
    });
    await this.cdp.send("Page.startScreencast", { format: "jpeg", quality: 95, everyNthFrame: 1 });
    await sleep(400);
    this.t0 = Date.now() / 1000;
  }
  mark(name) { this.markers[name] = +(Date.now() / 1000 - this.t0).toFixed(2); console.log(this.name, name, this.markers[name]); }
  async stop() {
    await sleep(800);
    await this.cdp.send("Page.stopScreencast");
    const end = Date.now() / 1000;
    const usable = this.frames.filter((f) => f.t >= this.t0 - 2);
    let list = "ffconcat version 1.0\n";
    usable.forEach((f, i) => {
      const next = usable[i + 1]?.t ?? end;
      const start = i === 0 ? this.t0 : f.t;
      list += `file '${f.file}'\nduration ${Math.max(0.001, next - start).toFixed(4)}\n`;
    });
    const listFile = join(this.dir, "list.txt");
    writeFileSync(listFile, list);
    const out = join(root, "public/clips", `${this.name}.mp4`);
    const r = spawnSync("ffmpeg", ["-y", "-loglevel", "error", "-safe", "0", "-f", "concat", "-i", listFile, "-vf", "fps=30,scale=trunc(iw/2)*2:trunc(ih/2)*2", "-c:v", "libx264", "-crf", "16", "-preset", "medium", "-g", "10", "-pix_fmt", "yuv420p", "-movflags", "+faststart", out], { stdio: "inherit" });
    if (r.status) throw new Error("ffmpeg failed");
    console.log(this.name, "frames", usable.length, "seconds", (end - this.t0).toFixed(1), out);
    return { markers: this.markers, seconds: +(end - this.t0).toFixed(2) };
  }
}

async function glide(page, locator, steps = 14) {
  const b = await locator.boundingBox();
  await page.mouse.move(b.x + b.width / 2, b.y + b.height / 2, { steps });
  await sleep(120);
}

async function scrollPanel(page, dy, ticks = 6) {
  await glide(page, page.locator('aside[aria-label="Agent panel"]'), 10);
  for (let i = 0; i < ticks; i++) { await page.mouse.wheel(0, dy / ticks); await sleep(40); }
}

async function say(page, text, ms = 26) {
  const box = page.getByLabel("Message Nori");
  await glide(page, box);
  await box.click();
  await box.pressSequentially(text, { delay: ms });
  await sleep(250);
  await page.keyboard.press("Enter");
}

async function desktop(browser) {
  const ctx = await browser.newContext({ viewport: { width: 2560, height: 1600 }, deviceScaleFactor: 1, timezoneId: "America/Los_Angeles", locale: "en-US" });
  const page = await ctx.newPage();
  const be = new Backend();
  await mount(page, be);
  await page.goto(ORIGIN, { waitUntil: "networkidle" });
  await page.locator("textarea").last().waitFor();
  await sleep(1200);
  await page.mouse.move(1600, 960);

  const film = new Film(page, "desktop");
  await film.start();
  await sleep(500);
  film.mark("type1");
  await say(page, TASK_A);
  film.mark("sent1");
  await sleep(1300);
  be.startRun("task-a", TASK_A);
  be.reply("On it. I'm opening the investor site on your computer now. Keep talking, I'll post the brief here.");
  await sleep(2100);
  film.mark("reply1");

  await glide(page, page.locator(".nova-activity-button"), 18);
  await page.locator(".nova-activity-button").click();
  film.mark("panel");
  be.event("task-a", "model", "Thinking");
  await sleep(700);
  be.event("task-a", "tool", "browser_navigate");
  be.artifact("task-a", "ir-home");
  be.event("task-a", "tool", "browser_screenshot");
  await sleep(2300);
  await scrollPanel(page, 1280);
  film.mark("screenshot");
  await sleep(2200);

  film.mark("type2");
  await say(page, "Also add the full-year guidance.", 30);
  await sleep(1200);
  film.mark("sent2");
  be.reply("Added. The brief will include full-year guidance.");
  await sleep(1900);

  film.mark("type3");
  await say(page, "Different question. What time was the Q2 call?", 26);
  await sleep(1100);
  be.reply("September 3 at 2:00 p.m. Pacific. The brief is still running.");
  film.mark("reply3");
  await sleep(2000);

  film.mark("type4");
  await say(page, TASK_B, 24);
  await sleep(1000);
  be.startRun("task-b", TASK_B);
  be.reply("Started that as a second task. Both are running.");
  be.event("task-b", "model", "Thinking");
  await sleep(1800);
  await scrollPanel(page, -6000, 3);
  const hide = page.getByRole("button", { name: /Hide task details/ }).first();
  await glide(page, hide);
  await hide.click();
  film.mark("two-tasks");
  be.event("task-b", "tool", "browser_navigate");
  be.artifact("task-b", "ir-salesforce");
  await sleep(2600);

  be.finish("task-a", "Brief written from the Q2 FY27 earnings release.");
  be.reply("Here's your brief, under 200 words with the numbers first.");
  await sleep(600);
  be.reply(BRIEF);
  film.mark("brief");
  await sleep(3000);
  be.finish("task-b", "Docusign and Salesforce launched IAM natively across Salesforce on October 13.");
  be.reply("Second task done: on October 13, Docusign and Salesforce launched IAM natively across Salesforce and Slack.");
  film.mark("task-b-done");
  await sleep(1800);

  const open = page.getByRole("button", { name: "Open document" }).last();
  await open.scrollIntoViewIfNeeded();
  await glide(page, open);
  await open.click();
  film.mark("document");
  await sleep(2400);
  await page.mouse.move(1280, 960, { steps: 8 });
  await sleep(1500);
  film.mark("end");
  const result = await film.stop();
  await ctx.close();
  return { ...result, session: be.session };
}

async function phone(browser) {
  const ctx = await browser.newContext({ viewport: { width: 390, height: 844 }, deviceScaleFactor: 3, isMobile: true, hasTouch: true, timezoneId: "America/Los_Angeles", locale: "en-US" });
  const page = await ctx.newPage();
  const be = new Backend();
  // The same thread, later: the phone opens on what the desktop just produced.
  const now = Date.now();
  be.session.messages.push(
    { id: "p1-user", run_id: "", role: "user", text: TASK_A, created_at: iso(now - 300e3) },
    { id: "p2", run_id: "", role: "assistant", text: "On it. I'm opening the investor site on your computer now. Keep talking, I'll post the brief here.", created_at: iso(now - 298e3) },
    { id: "p3", run_id: "", role: "assistant", text: BRIEF, created_at: iso(now - 120e3) },
  );
  await mount(page, be);
  await page.goto(ORIGIN, { waitUntil: "networkidle" });
  await page.locator("textarea").last().waitFor();
  await sleep(1200);
  const film = new Film(page, "phone");
  await film.start();
  await sleep(600);
  film.mark("type");
  const box = page.getByLabel("Message Nori");
  await box.tap();
  await box.pressSequentially("Give me the guidance in one line I can paste.", { delay: 34 });
  await sleep(300);
  film.mark("send");
  await page.keyboard.press("Enter");
  await sleep(1400);
  be.reply("FY27 revenue guidance is $3,499M to $3,507M, raised on September 3.");
  film.mark("reply");
  await sleep(3200);
  film.mark("end");
  const result = await film.stop();
  await ctx.close();
  return result;
}

const which = process.argv[2];
const browser = await chromium.launch({ channel: "chrome" });
const file = join(root, "public/clips/markers.json");
let all = {};
try { all = JSON.parse(readFileSync(file, "utf8")); } catch { /* first run */ }
if (!which || which === "desktop") { const { session, ...d } = await desktop(browser); all.desktop = d; writeFileSync(join(root, "capture/session.json"), JSON.stringify(session, null, 1)); }
if (!which || which === "phone") all.phone = await phone(browser);
writeFileSync(file, JSON.stringify(all, null, 1));
await browser.close();
