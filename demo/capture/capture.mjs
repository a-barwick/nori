// Plays the scripted snapshots (capture/run.json) through the real frontend (web/,
// on port 3211) one film frame at a time, so every message lands on its exact frame.
// /api/* is answered in the browser; nothing touches the runtime.
// Outputs public/clips/desktop.mp4 (film frames 300-870), phone.mp4 (960-1469) and markers.json.
//   node capture/capture.mjs [desktop|phone]
import { chromium } from "playwright-core";
import { spawnSync } from "node:child_process";
import { copyFileSync, mkdirSync, readFileSync, rmSync, writeFileSync } from "node:fs";
import { join, resolve } from "node:path";
import { tmpdir } from "node:os";
import { spring } from "remotion";

const ORIGIN = "http://127.0.0.1:3211";
const root = resolve(import.meta.dirname, "..");
const sleep = (ms) => new Promise((r) => setTimeout(r, ms));
const RUN = JSON.parse(readFileSync(join(root, "capture/run.json"), "utf8"));
const SCALE = 1920 / 1664; // CSS pixels to film pixels at camera zoom 1
const MARK = "M48 11C54.5 11 59.2 14.3 62.6 20L84.6 58.4C91.5 70.5 84.6 85 70.6 85H25.4C11.4 85 4.5 70.5 11.4 58.4L33.4 20C36.8 14.3 41.5 11 48 11Z";
const lerp = (a, b, t) => a + (b - a) * t;
const smooth = (t) => t * t * t * (t * (t * 6 - 15) + 10);
const clamp01 = (t) => Math.max(0, Math.min(1, t));
const popAt = (age) => spring({ frame: age, fps: 30, config: { damping: 12, stiffness: 190, mass: 0.7 } });

const CSS = `
*,*::before,*::after{transition:none!important;animation:none!important;scroll-behavior:auto!important}
[role=tooltip],[data-radix-popper-content-wrapper],.anim-pop,.arrive{display:none!important}
.film-card{display:flex;align-items:center;gap:12px;width:fit-content;margin:8px 0 0 44px;padding:12px 18px 12px 12px;border:1px solid var(--hairline-strong);border-radius:14px;background:#fff;font-size:15px;font-weight:600;color:var(--text-primary);box-shadow:0 3px 16px #26085a0d;transform-origin:left center}
.film-card i{display:grid;place-items:center;width:38px;height:38px;border-radius:10px;background:#eee5fc;flex-shrink:0}
.film-note{margin:6px 0 0 44px;font-size:14px;color:var(--text-secondary);transform-origin:left center}
.film-send{display:flex;align-items:center;gap:10px;margin:12px 0 0 44px;transform-origin:left center}
.film-send button{padding:11px 26px;border-radius:999px;background:#5000ff;color:#fff;font-size:16px;font-weight:650}
.film-send span{font-size:15px;font-weight:600;color:var(--text-primary)}
`;
const ROOM = ".nova-live-body .thread-mask>div{padding-bottom:1400px!important}"; // lets the film scroll a message anywhere
const markSvg = (size) => `<svg width="${size}" height="${size}" viewBox="0 0 96 96" style="flex-shrink:0"><path d="${MARK}" fill="#fdfcff"/><clipPath id="film-mark-${size}"><path d="${MARK}"/></clipPath><rect x="30" y="61" width="36" height="30" rx="3" fill="#101a30" clip-path="url(#film-mark-${size})"/><path d="${MARK}" fill="none" stroke="#190936" stroke-width="3" stroke-linejoin="round"/><ellipse cx="38.5" cy="46" rx="3.4" ry="4" fill="#190936"/><ellipse cx="57.5" cy="46" rx="3.4" ry="4" fill="#190936"/><path d="M44 53.5Q48 57.1 52 53.5" fill="none" stroke="#190936" stroke-width="2.6" stroke-linecap="round"/></svg>`;
const DOC = `<i><svg width="20" height="24" viewBox="0 0 20 24"><path d="M3 1h9l6 6v14a2 2 0 0 1-2 2H3a2 2 0 0 1-2-2V3a2 2 0 0 1 2-2z" fill="#5000ff"/><path d="M5 12h9M5 16h9" stroke="#fff" stroke-width="1.6"/></svg></i>`;
const CHART = `<i><svg width="24" height="24" viewBox="0 0 24 24"><path d="M3 21V13M9 21V8M15 21V11M21 21V3" stroke="#5000ff" stroke-width="3.4" stroke-linecap="round"/></svg></i>`;

class Stage {
  index = 0;
  typed = new Map(); // scripted user message id -> id the UI generated when sending it
  pops = [];
  dirty = true;
  constructor(page, name, firstFrame) { this.page = page; this.name = name; this.first = firstFrame; this.dir = join(tmpdir(), `nori-film-${name}`); this.count = 0; }
  session() {
    const { session } = RUN[this.index];
    return { ...session, messages: session.messages.map((m) => this.typed.has(m.id) ? { ...m, id: this.typed.get(m.id) } : m) };
  }
  async mount(clock, startIndex) {
    this.index = startIndex;
    await this.page.route("**/api/**", (route) => {
      const body = route.request().postDataJSON() ?? {};
      if (new URL(route.request().url()).pathname.endsWith("/turn")) this.typed.set(this.sending, `${body.id}-user`);
      return route.fulfill({ status: 200, contentType: "application/json", body: JSON.stringify(this.session()) });
    });
    await this.page.addInitScript(([css, now]) => {
      window.__now = now;
      Date.now = () => window.__now;
      Element.prototype.scrollTo = () => {}; // The film places the thread itself.
      addEventListener("DOMContentLoaded", () => {
        const style = document.createElement("style");
        style.textContent = css;
        document.documentElement.append(style);
      });
    }, [CSS + (this.name === "desktop" ? ".nova-backdrop{padding:40px 172px 144px!important}" : ROOM), clock]);
    await this.page.goto(ORIGIN, { waitUntil: "networkidle" });
    await this.page.getByLabel("Message Nori").waitFor();
    await sleep(800);
    rmSync(this.dir, { recursive: true, force: true });
    mkdirSync(this.dir, { recursive: true });
  }
  /** Bring in the snapshot for this frame, if there is one, and wait for React to draw it. */
  async snapshot(frame) {
    const i = RUN.findIndex((s) => s.frame === frame);
    if (i < 0) return [];
    const before = new Set(RUN[this.index].session.messages.map((m) => m.id));
    this.index = i;
    await this.page.evaluate((ts) => { window.__now = ts; dispatchEvent(new Event("online")); }, RUN[i].ts);
    await this.settle();
    return RUN[i].session.messages.filter((m) => !before.has(m.id));
  }
  async settle() { this.dirty = true; await sleep(140); await this.page.evaluate(() => new Promise((r) => requestAnimationFrame(() => requestAnimationFrame(r)))); }
  pop(selector, frame, origin) { this.pops.push({ selector, frame, origin }); }
  async runPops(frame) {
    const live = this.pops.filter((p) => frame - p.frame <= 24);
    if (!live.length) return;
    this.dirty = true;
    await this.page.evaluate((items) => { for (const { selector, scale, origin } of items) { const el = document.querySelector(selector); if (el) { el.style.scale = String(scale); if (origin) el.style.transformOrigin = origin; } } },
      live.map((p) => ({ selector: p.selector, origin: p.origin, scale: frame - p.frame >= 24 ? 1 : popAt(frame - p.frame) })));
  }
  /** One film frame. A frame where nothing changed reuses the last image, so holds are perfectly still. */
  async shoot() {
    const file = join(this.dir, `${String(this.count).padStart(5, "0")}.jpg`);
    if (this.dirty || !this.count) await this.page.screenshot({ path: file, type: "jpeg", quality: 96, caret: "hide" });
    else copyFileSync(join(this.dir, `${String(this.count - 1).padStart(5, "0")}.jpg`), file);
    this.count++;
    this.dirty = false;
  }
  encode(hold = 0) {
    const out = join(root, "public/clips", `${this.name}.mp4`);
    const r = spawnSync("ffmpeg", ["-y", "-loglevel", "error", "-framerate", "30", "-i", join(this.dir, "%05d.jpg"), "-vf", `scale=trunc(iw/2)*2:trunc(ih/2)*2,tpad=stop_mode=clone:stop=${hold}`, "-c:v", "libx264", "-crf", "15", "-preset", "medium", "-g", "10", "-pix_fmt", "yuv420p", "-movflags", "+faststart", out], { stdio: "inherit" });
    if (r.status) throw new Error("ffmpeg failed");
    console.log(this.name, this.count, "frames", out);
  }
}

const typeInto = async (page, text, from, to, frame, stage) => {
  if (frame < from || frame >= to) return;
  const want = Math.round(((frame - from + 1) / (to - from)) * text.length);
  await page.getByLabel("Message Nori").fill(text.slice(0, want));
  stage.dirty = true;
};

async function desktop(browser) {
  const ctx = await browser.newContext({ viewport: { width: 1664, height: 1040 }, deviceScaleFactor: 2, reducedMotion: "reduce", timezoneId: "America/Los_Angeles", locale: "en-US" });
  const page = await ctx.newPage();
  const stage = new Stage(page, "desktop", 300);
  await stage.mount(RUN[0].ts, 0);
  await page.evaluate(() => {
    const c = document.createElement("div");
    c.id = "film-cursor";
    c.innerHTML = '<svg width="26" height="26" viewBox="0 0 24 24"><path d="M5 3l14 8-6.5 1.6L9.5 19z" fill="#120a2b" stroke="#fff" stroke-width="1.6" stroke-linejoin="round"/></svg>';
    c.style.cssText = "position:fixed;left:0;top:0;z-index:2147483647;pointer-events:none;transform-origin:5px 3px";
    const caret = document.createElement("div");
    caret.id = "film-caret";
    caret.style.cssText = "position:fixed;width:2px;background:#5000ff;z-index:2147483646";
    document.documentElement.append(c, caret);
    window.__caret = () => {
      const box = document.querySelector('[aria-label="Message Nori"]');
      const r = box.getBoundingClientRect();
      const ctx = document.createElement("canvas").getContext("2d");
      ctx.font = getComputedStyle(box).font;
      caret.style.left = `${r.left + ctx.measureText(box.value).width + 1}px`;
      caret.style.top = `${r.top + 5}px`;
      caret.style.height = `${r.height - 10}px`;
    };
  });
  const center = async (locator) => { const b = await locator.boundingBox(); return [b.x + b.width / 2, b.y + b.height / 2]; };
  const rest = await center(page.getByLabel("Message Nori"));
  let path = [[300, rest[0] + 330, rest[1] + 8], [660, rest[0] + 330, rest[1] + 8]];
  const moveCursor = async (frame) => {
    const i = path.findIndex(([f]) => f > frame);
    const [fa, xa, ya] = path[Math.max(0, (i < 0 ? path.length : i) - 1)];
    const [fb, xb, yb] = path[i < 0 ? path.length - 1 : i];
    const t = fa === fb ? 1 : smooth((frame - fa) / (fb - fa));
    const x = lerp(xa, xb, t), y = lerp(ya, yb, t);
    await page.evaluate(([x, y]) => { document.getElementById("film-cursor").style.translate = `${x}px ${y}px`; }, [x, y]);
    if (frame > path[0][0] && fa !== fb) stage.dirty = true;
  };
  const MSG = `[data-item-id="${RUN.at(-1).session.messages[6].id}"]`;
  const groupOf = (el) => el.parentElement.parentElement;
  let pin, T, final = {};
  const rects = [];

  for (let frame = 300; frame <= 870; frame++) {
    await typeInto(page, "Grab numbers for the Acme account", 303, 343, frame, stage);
    if (frame <= 345) {
      await page.evaluate((frame) => { window.__caret(); document.getElementById("film-caret").style.opacity = frame === 345 ? 0 : (frame % 30 < 18 || frame > 302) ? 1 : 0; }, frame);
      if (frame <= 303) stage.dirty = true;
    }
    if (frame === 345) { stage.sending = RUN[1].session.messages[0].id; await page.getByLabel("Message Nori").press("Enter"); await sleep(60); }
    const fresh = await stage.snapshot(frame);
    for (const m of fresh) {
      const id = stage.typed.get(m.id) ?? m.id;
      stage.pop(`[data-item-id="${id}"]`, frame, m.role === "user" ? "right center" : "left center");
    }

    if (frame === 524) {
      const button = await center(page.locator(".nova-activity-button"));
      path = [[524, rest[0] + 330, rest[1] + 8], [538, ...button], [612, ...button]];
    }
    if (frame === 540) {
      await page.locator(".nova-activity-button").click();
      await sleep(60);
      await page.getByRole("button", { name: /Hide task details/ }).first().click();
      await page.evaluate(() => { const a = document.querySelector('aside[aria-label="Agent panel"]'); a.firstElementChild.style.minWidth = "var(--panel-w)"; a.style.width = "0px"; });
      await stage.settle();
    }
    if (frame >= 540 && frame <= 552) {
      await page.evaluate((t) => { const a = document.querySelector('aside[aria-label="Agent panel"]'); a.style.width = `calc(var(--panel-w) * ${t})`; }, smooth((frame - 540) / 12));
      stage.dirty = true;
    }
    if (frame === 612) {
      const close = await center(page.getByRole("button", { name: "Hide agent panel" }));
      const at = path.at(-1);
      path = [[612, at[1], at[2]], [628, ...close], [634, ...close], [660, close[0] + 30, 1100]];
    }
    if (frame >= 630 && frame <= 642) {
      await page.evaluate((t) => { const a = document.querySelector('aside[aria-label="Agent panel"]'); if (a) a.style.width = `calc(var(--panel-w) * ${1 - t})`; }, smooth((frame - 630) / 12));
      stage.dirty = true;
    }
    if (frame === 643) { await page.getByRole("button", { name: "Hide agent panel" }).evaluate((el) => el.click()); await stage.settle(); }
    await moveCursor(frame);
    if (frame === 720) {
      await page.evaluate(([selector, doc]) => {
        const card = document.createElement("div");
        card.className = "film-card";
        card.id = "film-doc";
        card.innerHTML = `${doc}Acme numbers`;
        document.querySelector(selector).parentElement.parentElement.append(card);
      }, [MSG, DOC]);
      stage.pop("#film-doc", 723, "left center");
      await stage.settle();
    }
    if (frame === 722) stage.dirty = true;
    await stage.runPops(frame);

    if (frame === 809) {
      pin = await page.evaluate((s) => { const r = document.querySelector(s).getBoundingClientRect(); return { x: r.left, y: r.top, w: r.width, h: r.height }; }, MSG);
      T = pin.y + pin.h / 2 - 422;
      final.camera = { tx: 960 - SCALE * (pin.x + 44 + (pin.w - 44) / 2), ty: 540 - SCALE * (pin.y + pin.h / 2) };
      final.message = pin;
      await page.evaluate((ROOM) => { const s = document.createElement("style"); s.textContent = ROOM + ".nova-live-body .thread-mask>div{padding-left:var(--pad)!important;padding-right:var(--pad)!important}.nova-composer>div{padding-left:var(--pad)!important;padding-right:var(--pad)!important}"; document.documentElement.append(s); }, ROOM);
    }
    if (frame >= 810) {
      // The film masks to the smooth rectangle; the page snaps to whole pixels underneath it,
      // so the pinned message and its medallion never land on a new pixel.
      const t = smooth(clamp01((frame - 810) / 60));
      const w = lerp(1320, 390, t), h = lerp(856, 844, t), top = lerp(40, T, t), r = lerp(22, 44, t), pad = lerp(40, 18, t);
      const inset = (width, p) => Math.max((width - 920) / 2, 0) + p;
      const wReal = 2 * Math.round(w / 2), padReal = Math.round(pad);
      const rect = await page.evaluate(({ w, h, top, r, left, pad, header, pin, selector }) => {
        const win = document.querySelector(".nova-window");
        document.querySelector(".nova-backdrop").style.background = "#fff";
        document.documentElement.style.setProperty("--pad", `${pad}px`);
        document.querySelector(".nova-header").style.opacity = header;
        Object.assign(win.style, { position: "fixed", maxWidth: "none", margin: "0", borderColor: "transparent", boxShadow: "none", width: `${w}px`, height: `${h}px`, top: `${top}px`, left: `${left}px`, borderRadius: `${r}px` });
        const msg = document.querySelector(selector);
        win.style.left = `${left + pin.x - msg.getBoundingClientRect().left}px`;
        const scroller = win.querySelector(".thread-mask");
        scroller.scrollTop += msg.getBoundingClientRect().top - pin.y;
        const m = msg.getBoundingClientRect();
        return { errX: m.left - pin.x, errY: m.top - pin.y };
      }, { w: wReal, h: Math.ceil(h) + 2, top: Math.floor(top), r, left: pin.x - inset(wReal, padReal), pad: padReal, header: 1 - clamp01((frame - 810) / 36), pin, selector: MSG });
      Object.assign(rect, { x: pin.x - inset(w, pad), y: top, w, h, r });
      await page.evaluate(() => new Promise((r) => requestAnimationFrame(r)));
      rects.push(rect);
      if (Math.abs(rect.errX) > 0.01 || Math.abs(rect.errY) > 0.5) console.log("drift", frame, rect.errX, rect.errY);
      stage.dirty = true;
    }
    await stage.shoot();
    if (frame % 60 === 0) console.log("desktop frame", frame);
  }
  stage.encode(120); // the phone screen holds until it goes dark
  final.window = rects;
  await ctx.close();
  return final;
}

async function phone(browser) {
  const ctx = await browser.newContext({ viewport: { width: 390, height: 844 }, deviceScaleFactor: 3, isMobile: true, hasTouch: true, reducedMotion: "reduce", timezoneId: "America/Los_Angeles", locale: "en-US" });
  const page = await ctx.newPage();
  const stage = new Stage(page, "phone", 960);
  const before = RUN.findIndex((s) => s.frame === 990) - 1; // the thread as it stood at the end of the evening
  stage.index = before;
  await stage.mount(Date.parse("2027-02-17T16:00:00Z"), before);
  await page.evaluate(([doc, selector]) => {
    const card = document.createElement("div");
    card.className = "film-card";
    card.innerHTML = `${doc}Acme numbers`;
    document.querySelector(selector).parentElement.parentElement.append(card);
  }, [DOC, `[data-item-id="${RUN[before].session.messages[6].id}"]`]);
  const morning = RUN[RUN.findIndex((s) => s.frame === 990)].session.messages[7].id;
  let info = {};

  for (let frame = 960; frame <= 1469; frame++) {
    if (frame === 1230) { stage.sending = RUN.at(-1).session.messages[8].id; await page.getByLabel("Message Nori").press("Enter"); await sleep(60); }
    await stage.snapshot(frame);
    if (frame === 990) {
      // Put the morning's message just under the header, with room for what follows it.
      await page.evaluate((id) => {
        const scroller = document.querySelector(".thread-mask");
        const el = document.querySelector(`[data-item-id="${id}"]`);
        scroller.scrollTop += el.getBoundingClientRect().top - 118;
      }, morning);
      await stage.settle();
    }
    if (frame === 1110) {
      await page.evaluate(([id, mark]) => {
        const row = document.createElement("div");
        row.className = "film-send";
        row.id = "film-send";
        row.innerHTML = `${mark}<button>Send it</button>`;
        document.querySelector(`[data-item-id="${id}"]`).parentElement.parentElement.append(row);
        const dot = document.createElement("div");
        dot.id = "film-tap";
        dot.style.cssText = "position:fixed;width:44px;height:44px;margin:-22px;border-radius:50%;background:#19093640;opacity:0;z-index:99";
        document.documentElement.append(dot);
      }, [morning, markSvg(30)]);
      stage.pop("#film-send", 1110, "left center");
      await stage.settle();
    }
    if (frame >= 1164 && frame <= 1176) {
      await page.evaluate((frame) => {
        const b = document.querySelector("#film-send button");
        const dot = document.getElementById("film-tap");
        if (b) { const r = b.getBoundingClientRect(); dot.style.left = `${r.left + r.width / 2}px`; dot.style.top = `${r.top + r.height / 2}px`; }
        dot.style.opacity = frame < 1170 ? (frame - 1164) / 6 : Math.max(0, 1 - (frame - 1170) / 6);
        dot.style.scale = frame < 1170 ? 1.2 - (frame - 1164) * 0.05 : 0.9 + (frame - 1170) * 0.1;
      }, frame);
      stage.dirty = true;
    }
    if (frame === 1170) {
      await page.evaluate((mark) => { document.getElementById("film-send").innerHTML = `${mark}<span>Sent to Priya at Acme.</span>`; }, markSvg(30));
      stage.pop("#film-send", 1170, "left center");
    }
    if (frame === 1200) await page.getByLabel("Message Nori").evaluate((el) => el.focus());
    await typeInto(page, "Amazing. Now turn the Acme numbers into a dashboard and share it with Sam.", 1200, 1229, frame, stage);
    if (frame === 1405) {
      // The dashboard flies into this spot, so measure it before the card appears.
      await page.evaluate((chart) => {
        const log = document.querySelector('[role="log"] > div[aria-live="off"]');
        const card = document.createElement("div");
        card.className = "film-card";
        card.id = "film-dash";
        card.style.visibility = "hidden";
        card.style.margin = "14px 0 0";
        card.innerHTML = `${chart}<span>Acme renewal dashboard.<br>Shared with Sam.</span>`;
        const note = document.createElement("div");
        note.className = "film-note";
        note.id = "film-sam";
        note.style.margin = "8px 0 0 4px";
        note.style.visibility = "hidden";
        note.textContent = "Sam opened it.";
        log.append(card, note);
      }, CHART);
      info.card = await page.evaluate(() => { const r = document.getElementById("film-dash").getBoundingClientRect(); return { x: r.left, y: r.top, w: r.width, h: r.height }; });
      stage.dirty = true;
    }
    if (frame === 1410) {
      await page.evaluate(() => { document.getElementById("film-dash").style.visibility = "visible"; });
      stage.pop("#film-dash", 1410, "left center");
      stage.dirty = true;
    }
    if (frame === 1440) {
      await page.evaluate(() => { document.getElementById("film-sam").style.visibility = "visible"; });
      stage.pop("#film-sam", 1440, "left center");
      stage.dirty = true;
    }
    if (frame === 1462) { // Remotion flies a copy of the card off from here
      await page.evaluate(() => { document.getElementById("film-dash").style.visibility = "hidden"; });
      stage.dirty = true;
    }
    await stage.runPops(frame);
    await stage.shoot();
    if (frame % 60 === 0) console.log("phone frame", frame);
  }
  stage.encode();
  await ctx.close();
  return info;
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
