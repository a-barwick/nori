// Replays a recorded real Nori run (capture/run.json, polled from the live runtime
// on 2026-10-31) through the real frontend (web/, on port 3211) and films it.
// /api/* is answered in the browser from those snapshots; nothing touches the
// runtime. Outputs public/clips/{desktop,phone}.mp4 and markers.json.
//   node capture/capture.mjs [desktop|phone]
import { chromium } from "playwright-core";
import { spawnSync } from "node:child_process";
import { mkdirSync, readFileSync, rmSync, writeFileSync } from "node:fs";
import { join, resolve } from "node:path";
import { tmpdir } from "node:os";

const ORIGIN = "http://127.0.0.1:3211";
const root = resolve(import.meta.dirname, "..");
const sleep = (ms) => new Promise((r) => setTimeout(r, ms));
const RUN = JSON.parse(readFileSync(join(root, "capture/run.json"), "utf8"));

class Replay {
  index = 0;
  ids = new Map(); // real user message id -> id the UI generated when "sending" it
  shown = new Set();
  current;
  apply(i) {
    this.index = i;
    const { session } = RUN[i];
    this.current = { ...session, messages: session.messages.map((m) => this.ids.has(m.id) ? { ...m, id: this.ids.get(m.id) } : m) };
    for (const m of session.messages) this.shown.add(m.id);
  }
  /** Messages this snapshot adds that the previous one lacked. */
  fresh(i) { return RUN[i].session.messages.filter((m) => !this.shown.has(m.id)); }
  hidden(m) { return RUN[this.index].session.dispatches.some((d) => m.id === `${d.id}-user`) || (m.run_id && m.id === `${m.run_id}-user`); }
}

async function mount(page, replay) {
  await page.route("**/api/**", async (route) => {
    const op = new URL(route.request().url()).pathname.split("/").pop();
    const body = route.request().postDataJSON() ?? {};
    if (op === "artifact") {
      return route.fulfill({ status: 200, contentType: "image/png", body: readFileSync(join(root, "public/real", `${body.artifact_id}.png`)) });
    }
    if (op === "turn") {
      replay.ids.set(replay.sending, `${body.id}-user`);
      replay.apply(replay.index);
    }
    return route.fulfill({ status: 200, contentType: "application/json", body: JSON.stringify(replay.current) });
  });
  await page.addInitScript(() => {
    const real = Date.now.bind(Date);
    window.__dt = 0;
    Date.now = () => real() + window.__dt;
    addEventListener("DOMContentLoaded", () => {
      const style = document.createElement("style");
      style.textContent = "[role=tooltip],[data-radix-popper-content-wrapper]{display:none!important}" + (innerWidth > 2000 ? "html{zoom:2}" : "");
      document.documentElement.append(style);
      if (matchMedia("(pointer: coarse)").matches) return;
      // A visible pointer: headless screencasts do not draw one.
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

const panel = (page) => page.locator('aside[aria-label="Agent panel"]');

async function scrollPanel(page, dy, ticks = 6) {
  await glide(page, panel(page), 10);
  for (let i = 0; i < ticks; i++) { await page.mouse.wheel(0, dy / ticks); await sleep(40); }
}

/** Push snapshot i to the page: set its clock to that moment and ask it to refresh. */
async function show(page, replay, i) {
  replay.apply(i);
  await page.evaluate((ts) => { window.__dt = ts - (Date.now() - window.__dt); dispatchEvent(new Event("online")); }, RUN[i].ts);
}

async function type(page, replay, message, film, ms) {
  film?.mark(`type:${message.text.slice(0, 12)}`);
  const box = page.getByLabel("Message Nori");
  if (!page.viewportSize() || page.viewportSize().width > 500) await glide(page, box);
  await box.click();
  await box.pressSequentially(message.text, { delay: ms });
  await sleep(220);
  replay.sending = message.id;
  await page.keyboard.press("Enter");
  film?.mark(`sent:${message.text.slice(0, 12)}`);
}

async function open(page, replay, first) {
  replay.apply(first);
  await page.goto(ORIGIN, { waitUntil: "networkidle" });
  await page.getByLabel("Message Nori").waitFor();
  await page.evaluate((ts) => { window.__dt = ts - (Date.now() - window.__dt); }, RUN[first].ts);
  await sleep(1200);
}

// How long each real moment stays on screen, in milliseconds of film.
const HOLD = (text) => (text.length > 600 ? 3600 : text.length > 120 ? 2600 : 1900);

async function desktop(browser) {
  const ctx = await browser.newContext({ viewport: { width: 2560, height: 1600 }, deviceScaleFactor: 1, timezoneId: "America/Los_Angeles", locale: "en-US" });
  const page = await ctx.newPage();
  const replay = new Replay();
  const start = RUN.findIndex((s) => s.session.messages.some((m) => m.text.startsWith("Got it! For the rest")));
  await mount(page, replay);
  await open(page, replay, start);
  await page.mouse.move(1600, 960);
  const film = new Film(page, "desktop");
  await film.start();
  await sleep(500);
  let panelOpen = false, shot = false;

  for (let i = start + 1; i < RUN.length; i++) {
    const fresh = replay.fresh(i);
    if (fresh.some((m) => m.text.startsWith("Give me the full-year"))) break;
    const user = fresh.find((m) => m.role === "user" && !RUN[i].session.dispatches.some((d) => m.id === `${d.id}-user`) && !(m.run_id && m.id === `${m.run_id}-user`));
    if (user) {
      await page.evaluate((ts) => { window.__dt = ts - (Date.now() - window.__dt); }, Date.parse(user.created_at));
      await type(page, replay, user, film, user.text.length > 90 ? 11 : 26);
      replay.index = i;
    }
    await show(page, replay, i);
    const said = fresh.filter((m) => m.role === "assistant");
    for (const m of said) {
      film.mark(`reply:${m.text.slice(0, 12)}`);
      await sleep(HOLD(m.text));
    }
    if (!said.length) await sleep(user ? 900 : 70);

    if (!panelOpen && said.some((m) => m.text.startsWith("I'm starting a second task"))) {
      panelOpen = true;
      const button = page.locator(".nova-activity-button");
      await glide(page, button, 16);
      await button.click();
      film.mark("panel");
      await sleep(700);
      const hide = page.getByRole("button", { name: /Hide task details/ }).first();
      await glide(page, hide);
      await hide.click();
      film.mark("two-tasks");
      await sleep(2000);
    }
    const shots = RUN[i].session.runs.find((r) => r.id.startsWith("b2b631a1"))?.artifacts?.length;
    if (panelOpen && !shot && shots) {
      shot = true;
      const card = panel(page).locator("article").last().getByRole("button").first();
      await card.scrollIntoViewIfNeeded();
      await glide(page, card);
      await card.click();
      await sleep(500);
      await page.locator('img[alt^="Screenshot"]').first().waitFor();
      await page.locator('img[alt^="Screenshot"]').first().scrollIntoViewIfNeeded();
      film.mark("screenshot");
      await sleep(3000);
      const close = page.getByRole("button", { name: "Hide agent panel" });
      await glide(page, close, 12);
      await close.click();
      panelOpen = "closed";
      film.mark("panel-closed");
    }
  }
  await sleep(1500);
  const doc = page.getByRole("button", { name: "Open document" }).last();
  await doc.scrollIntoViewIfNeeded();
  await glide(page, doc);
  await doc.click();
  film.mark("document");
  await sleep(2400);
  await page.mouse.move(1280, 960, { steps: 8 });
  await sleep(1200);
  film.mark("end");
  const result = await film.stop();
  await ctx.close();
  return result;
}

async function phone(browser) {
  const ctx = await browser.newContext({ viewport: { width: 390, height: 844 }, deviceScaleFactor: 3, isMobile: true, hasTouch: true, timezoneId: "America/Los_Angeles", locale: "en-US" });
  const page = await ctx.newPage();
  const replay = new Replay();
  const ask = RUN.findIndex((s) => s.session.messages.some((m) => m.text.startsWith("Give me the full-year")));
  await mount(page, replay);
  await open(page, replay, ask - 1);
  const film = new Film(page, "phone");
  await film.start();
  await sleep(600);
  film.mark("type");
  const user = RUN[ask].session.messages.find((m) => m.text.startsWith("Give me the full-year"));
  await type(page, replay, user, null, 30);
  replay.index = ask;
  for (let i = ask; i < RUN.length; i++) {
    await show(page, replay, i);
    await sleep(RUN[i].session.conversation.processing ? 900 : 120);
  }
  film.mark("reply");
  await sleep(3400);
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
if (!which || which === "desktop") all.desktop = await desktop(browser);
if (!which || which === "phone") all.phone = await phone(browser);
writeFileSync(file, JSON.stringify(all, null, 1));
await browser.close();
