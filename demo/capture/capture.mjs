// Plays the scripted snapshots (capture/run.json) through the real frontend (web/,
// on port 3211) one film frame at a time, so every message lands on its exact frame.
// /api/* is answered in the browser; nothing touches the runtime.
// Outputs public/clips/desktop.mp4 (film frames 540-1365), phone.mp4 (1485-2549) and markers.json.
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
.film-pending{border:2px dashed #5000ff!important}
.film-done{border:2px solid #5000ff!important}
.film-invite{display:flex;gap:14px;width:440px;max-width:calc(100% - 44px);box-sizing:border-box;margin:8px 0 10px 44px;padding:10px;border-radius:18px;background:#fff;box-shadow:0 3px 16px #26085a12;transform-origin:left center}
.film-invite svg.map{width:min(150px,30%);height:112px;border-radius:10px;flex-shrink:0;display:block}
.film-invite .side{flex:1;display:flex;flex-direction:column;justify-content:space-between;gap:8px;min-width:0}
.film-invite strong{font-size:15px;font-weight:700;color:var(--text-primary)}
.film-faces{display:flex}
.film-faces b{position:relative;display:grid;place-items:center;width:40px;height:40px;margin-right:-6px;border-radius:50%;border:2px solid #fff;color:#fff;font-size:15px;font-weight:700}
.film-faces u{position:absolute;right:-3px;bottom:-3px;display:grid;place-items:center;width:18px;height:18px;border-radius:50%;background:#1fa463;border:2px solid #fff;color:#fff;font-size:10px;text-decoration:none;visibility:hidden}
.film-btn{padding:10px 14px;border-radius:999px;background:#5000ff;color:#fff;font-size:14px;font-weight:650;text-align:center;white-space:nowrap}
.film-state{padding:10px 0;font-size:15px;font-weight:650;color:var(--text-primary)}
.film-proposal{width:280px;margin:10px 0 14px 44px;padding:10px;border-radius:16px;background:#fff;transform-origin:left center;box-shadow:0 3px 16px #26085a12}
.film-proposal .page{height:92px;padding:10px 12px;border-radius:8px;background:#f6f2ff;display:flex;flex-direction:column;gap:7px;margin-bottom:9px}
.film-proposal .page s{display:block;height:5px;border-radius:3px;background:#d8ccf5;text-decoration:none}
.film-proposal strong{display:block;font-size:15px;font-weight:700;color:var(--text-primary)}
.film-proposal span{display:block;margin-top:1px;font-size:13px;color:var(--text-secondary)}
.film-proposal .film-btn{margin-top:10px}
.film-sheet{position:fixed;inset:0;z-index:60;background:#f3edff;padding:62px 16px 26px;display:flex;flex-direction:column;font-family:inherit;color:#190936;overflow:hidden}
.film-sheet .doc{flex:1;padding:24px 22px;border-radius:18px;background:#fff;overflow:hidden}
.film-sheet h3{margin:0;font-size:30px;line-height:1.15;font-weight:800;letter-spacing:-.5px}
.film-sheet h4{margin:30px 0 10px;font-size:20px;font-weight:700}
.film-sheet p{margin:9px 0 0;font-size:18px;line-height:1.4;color:#4a4060}
.film-sheet table{width:100%;margin-top:6px;border-collapse:collapse;font-size:18px}
.film-sheet td{padding:13px 0;border-top:1px solid #ece6f7}
.film-sheet td:last-child{text-align:right;font-weight:700}
.film-sheet .film-btn{margin-top:14px;padding:18px;font-size:20px}
`;
const ROOM = ".nova-live-body .thread-mask>div{padding-bottom:1400px!important}"; // lets the film scroll a message anywhere
const markSvg = (size) => `<svg width="${size}" height="${size}" viewBox="0 0 96 96" style="flex-shrink:0"><path d="${MARK}" fill="#fdfcff"/><clipPath id="film-mark-${size}"><path d="${MARK}"/></clipPath><rect x="30" y="61" width="36" height="30" rx="3" fill="#101a30" clip-path="url(#film-mark-${size})"/><path d="${MARK}" fill="none" stroke="#190936" stroke-width="3" stroke-linejoin="round"/><ellipse cx="38.5" cy="46" rx="3.4" ry="4" fill="#190936"/><ellipse cx="57.5" cy="46" rx="3.4" ry="4" fill="#190936"/><path d="M44 53.5Q48 57.1 52 53.5" fill="none" stroke="#190936" stroke-width="2.6" stroke-linecap="round"/></svg>`;
const APP = `<i><svg width="24" height="24" viewBox="0 0 24 24"><path d="M3 5h18M3 12h18M3 19h18" stroke="#5000ff" stroke-width="3.4" stroke-linecap="round"/></svg></i>`;
const MAP = `<svg class="map" viewBox="0 0 150 112" preserveAspectRatio="xMidYMid slice"><rect width="150" height="112" fill="#eee5fc"/><path d="M0 34H150M0 80H150M42 0V112M108 0V112" stroke="#fff" stroke-width="9"/><path d="M42 80H108" stroke="#a98bff" stroke-width="4" stroke-dasharray="1 8" stroke-linecap="round"/><circle cx="42" cy="80" r="9" fill="#ff4b62" stroke="#fff" stroke-width="3"/><circle cx="108" cy="80" r="9" fill="#5000ff" stroke="#fff" stroke-width="3"/></svg>`;
const FACES = [["M", "#5000ff"], ["J", "#ff4b62"], ["D", "#1a8f8a"], ["R", "#d9822b"], ["K", "#3f7cf0"]].map(([l, c]) => `<b style="background:${c}">${l}<u>✓</u></b>`).join("");

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
    }, [CSS + (this.name === "desktop" ? ".nova-backdrop{padding:300px 312px 340px!important}" : ROOM), clock]);
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

/** The frontend's "Nori is thinking" line, with its mark and three dots moving per frame. */
const thinking = async (page, stage, frame) => {
  const on = await page.evaluate((frame) => {
    const line = document.querySelector('[role="status"]');
    const label = line?.querySelector("span");
    const details = line?.querySelector("button");
    let dots = document.getElementById("film-dots");
    if (!label?.textContent.startsWith("Nori is thinking")) {
      if (dots) dots.remove();
      if (details) details.style.display = "";
      return false;
    }
    if (details) details.style.display = "none";
    if (label.textContent !== "Nori is thinking") label.textContent = "Nori is thinking";
    if (!dots) {
      dots = document.createElement("span");
      dots.id = "film-dots";
      dots.style.cssText = "display:inline-flex;gap:3px;margin-left:-3px;align-items:center";
      dots.innerHTML = "<i></i><i></i><i></i>";
      for (const dot of dots.children) dot.style.cssText = "display:block;width:5px;height:5px;border-radius:50%;background:#5000ff";
      label.after(dots);
    }
    [...dots.children].forEach((dot, i) => { dot.style.translate = `0 ${-5 * Math.max(0, Math.sin((frame / 15 - i / 4) * Math.PI))}px`; });
    const mark = line.firstElementChild;
    if (mark) mark.style.translate = `0 ${-2 * Math.abs(Math.sin((frame / 15) * Math.PI))}px`;
    return true;
  }, frame);
  if (on) stage.dirty = true;
};

const typeInto = async (page, text, from, to, frame, stage) => {
  if (frame < from || frame >= to) return;
  const want = Math.round(((frame - from + 1) / (to - from)) * text.length);
  await page.getByLabel("Message Nori").fill(text.slice(0, want));
  stage.dirty = true;
};

/** A finger's tap, on phone frames `at - 6` to `at + 6`. */
const tapDot = async (page, stage, selector, frame, at) => {
  if (frame < at - 6 || frame > at + 6) return;
  await page.evaluate(([selector, age]) => {
    let dot = document.getElementById("film-tap");
    if (!dot) {
      dot = document.createElement("div");
      dot.id = "film-tap";
      dot.style.cssText = "position:fixed;width:44px;height:44px;margin:-22px;border-radius:50%;background:#19093640;z-index:99";
      document.documentElement.append(dot);
    }
    const el = document.querySelector(selector);
    if (el) { const r = el.getBoundingClientRect(); dot.style.left = `${r.left + r.width / 2}px`; dot.style.top = `${r.top + r.height / 2}px`; }
    dot.style.opacity = age < 0 ? (age + 6) / 6 : Math.max(0, 1 - age / 6);
    dot.style.scale = age < 0 ? 1.2 + age * -0.05 - 0.3 : 0.9 + age * 0.1;
  }, [selector, frame - at]);
  stage.dirty = true;
};


async function desktop(browser) {
  const ctx = await browser.newContext({ viewport: { width: 1664, height: 1200 }, deviceScaleFactor: 2, reducedMotion: "reduce", timezoneId: "America/Los_Angeles", locale: "en-US" });
  const page = await ctx.newPage();
  const stage = new Stage(page, "desktop", 540);
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
  let path = [[540, rest[0] + 330, rest[1] + 8], [1015, rest[0] + 330, rest[1] + 8]];
  const moveCursor = async (frame) => {
    const i = path.findIndex(([f]) => f > frame);
    const [fa, xa, ya] = path[Math.max(0, (i < 0 ? path.length : i) - 1)];
    const [fb, xb, yb] = path[i < 0 ? path.length - 1 : i];
    const t = fa === fb ? 1 : smooth((frame - fa) / (fb - fa));
    const x = lerp(xa, xb, t), y = lerp(ya, yb, t);
    await page.evaluate(([x, y]) => { document.getElementById("film-cursor").style.translate = `${x}px ${y}px`; }, [x, y]);
    if (frame > path[0][0] && fa !== fb) stage.dirty = true;
  };
  const MSG = `[data-item-id="${RUN.at(-1).session.messages[7].id}"]`;
  const KAZE = `[data-item-id="${RUN.at(-1).session.messages[6].id}"]`;
  const sends = [
    { text: "Grab numbers for the Acme account", from: 543, to: 583, send: 585 },
    { text: "Also find a dinner spot near their office", from: 627, to: 657, send: 660 },
    { text: "Also what's the weather there?", from: 663, to: 688, send: 690 },
  ];
  let pin, T, final = {};
  const rects = [];

  for (let frame = 540; frame <= 1365; frame++) {
    for (const { text, from, to } of sends) await typeInto(page, text, from, to, frame, stage);
    if (frame <= 690) {
      const typing = sends.some(({ from, send }) => frame >= from && frame < send);
      await page.evaluate((typing) => { window.__caret(); document.getElementById("film-caret").style.opacity = typing ? 1 : 0; }, typing);
    }
    const sending = sends.find(({ send }) => send === frame);
    if (sending) { stage.sending = RUN.find((s) => s.frame === frame).session.messages.at(-1).id; await page.getByLabel("Message Nori").press("Enter"); await sleep(60); }
    const fresh = await stage.snapshot(frame);
    for (const m of fresh) {
      const id = stage.typed.get(m.id) ?? m.id;
      stage.pop(`[data-item-id="${id}"]`, frame, m.role === "user" ? "right center" : "left center");
    }

    if (frame === 990) {
      await page.evaluate(([selector, map, faces]) => {
        const card = document.createElement("div");
        card.className = "film-invite film-pending";
        card.id = "film-invite";
        card.innerHTML = `${map}<div class="side"><strong>Table for 6, 7PM</strong><div class="film-faces">${faces}</div><div class="film-btn">Book it and invite the team</div></div>`;
        document.querySelector(selector).parentElement.insertAdjacentElement("afterend", card);
      }, [KAZE, MAP, FACES]);
      stage.pop("#film-invite", 990, "left center");
      await stage.settle();
    }
    if (frame === 1015) {
      const button = await center(page.locator("#film-invite .film-btn"));
      path = [[1015, rest[0] + 330, rest[1] + 8], [1062, ...button], [1085, ...button], [1130, rest[0] + 330, rest[1] + 8]];
    }
    if (frame === 1065) {
      await page.evaluate(() => {
        const card = document.getElementById("film-invite");
        card.className = "film-invite film-done";
        card.querySelector(".film-btn").outerHTML = '<div class="film-state">Booked. Invites sent.</div>';
      });
      stage.pop("#film-invite .film-state", 1065, "left center");
      await stage.settle();
    }
    if (frame >= 1080 && frame <= 1140 && (frame - 1080) % 15 === 0) {
      await page.evaluate((n) => { document.querySelectorAll("#film-invite u")[n].style.visibility = "visible"; }, (frame - 1080) / 15);
      stage.dirty = true;
    }
    await moveCursor(frame);
    if (frame === 1131) { await page.evaluate(() => { document.getElementById("film-cursor").style.opacity = 0; }); stage.dirty = true; }
    await stage.runPops(frame);
    await thinking(page, stage, frame);
    if (frame < 1304) {
      // The thread follows the newest message itself, easing rather than jumping.
      const moved = await page.evaluate(() => {
        const el = document.querySelector(".thread-mask");
        const target = Math.max(0, el.scrollHeight - el.clientHeight);
        const now = window.__scroll ?? 0;
        const next = Math.abs(target - now) < 1 ? target : Math.round(now + (target - now) * 0.2);
        const moved = next !== now || el.scrollTop !== next;
        window.__scroll = next;
        el.scrollTop = next;
        return moved;
      });
      if (moved) stage.dirty = true;
    }

    if (frame === 1304) {
      pin = await page.evaluate((s) => { const r = document.querySelector(s).getBoundingClientRect(); return { x: r.left, y: r.top, w: r.width, h: r.height }; }, MSG);
      T = pin.y + pin.h / 2 - 422;
      final.camera = { tx: 960 - SCALE * (pin.x + 44 + (pin.w - 44) / 2), ty: 540 - SCALE * (pin.y + pin.h / 2) };
      final.message = pin;
      await page.evaluate((ROOM) => { const s = document.createElement("style"); s.textContent = ROOM + ".nova-live-body .thread-mask>div{padding-left:var(--pad)!important;padding-right:var(--pad)!important}.nova-composer>div{padding-left:var(--pad)!important;padding-right:var(--pad)!important}"; document.documentElement.append(s); }, ROOM);
    }
    if (frame >= 1305) {
      // The film masks to the smooth rectangle; the page snaps to whole pixels underneath it,
      // so the pinned message and its medallion never land on a new pixel.
      const t = smooth(clamp01((frame - 1305) / 60));
      const w = lerp(1040, 390, t), h = lerp(560, 844, t), top = lerp(300, T, t), r = lerp(22, 44, t), pad = lerp(40, 18, t);
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
      }, { w: wReal, h: Math.ceil(h) + 2, top: Math.floor(top), r, left: pin.x - inset(wReal, padReal), pad: padReal, header: 1 - clamp01((frame - 1305) / 36), pin, selector: MSG });
      Object.assign(rect, { x: pin.x - inset(w, pad), y: top, w, h, r });
      await page.evaluate(() => new Promise((r) => requestAnimationFrame(r)));
      rects.push(rect);
      if (Math.abs(rect.errX) > 0.01 || Math.abs(rect.errY) > 0.5) console.log("drift", frame, rect.errX, rect.errY);
      stage.dirty = true;
    }
    await stage.shoot();
    if (frame % 60 === 0) console.log("desktop frame", frame);
  }
  stage.encode(130); // the phone screen holds until it goes dark
  final.window = rects;
  await ctx.close();
  return final;
}

async function phone(browser) {
  const ctx = await browser.newContext({ viewport: { width: 390, height: 844 }, deviceScaleFactor: 3, isMobile: true, hasTouch: true, reducedMotion: "reduce", timezoneId: "America/Los_Angeles", locale: "en-US" });
  const page = await ctx.newPage();
  const stage = new Stage(page, "phone", 1485);
  const next = RUN.findIndex((s) => s.frame === 1615);
  const before = next - 1; // the thread as it stood at the end of the evening
  stage.index = before;
  await stage.mount(Date.parse("2027-02-17T16:00:00Z"), before);
  await page.evaluate(([map, faces, selector]) => {
    const card = document.createElement("div");
    card.className = "film-invite film-done";
    card.innerHTML = `${map}<div class="side"><strong>Table for 6, 7PM</strong><div class="film-faces">${faces}</div><div class="film-state">Booked.</div></div>`;
    card.querySelectorAll("u").forEach((u) => { u.style.visibility = "visible"; });
    document.querySelector(selector).parentElement.insertAdjacentElement("afterend", card);
  }, [MAP, FACES, `[data-item-id="${RUN[before].session.messages[6].id}"]`]);
  const last = RUN.at(-1).session.messages;
  const morning = last[8].id;
  let info = {};

  for (let frame = 1485; frame <= 2549; frame++) {
    if (frame === 2115) { stage.sending = last[9].id; await page.getByLabel("Message Nori").press("Enter"); await sleep(60); }
    const fresh = await stage.snapshot(frame);
    for (const m of fresh) if (frame !== 1615) stage.pop(`[data-item-id="${stage.typed.get(m.id) ?? m.id}"]`, frame, m.role === "user" ? "right center" : "left center");
    if (frame === 1615) {
      // Put the morning's message just under the header, with room for what follows it.
      await page.evaluate((id) => {
        const scroller = document.querySelector(".thread-mask");
        const el = document.querySelector(`[data-item-id="${id}"]`);
        scroller.scrollTop += el.getBoundingClientRect().top - 118;
      }, morning);
      await stage.settle();
    }
    if (frame === 1650) {
      await page.evaluate((id) => {
        const card = document.createElement("div");
        card.id = "film-proposal";
        card.className = "film-proposal film-pending";
        card.innerHTML = "<div class=\"page\"><s style=\"width:55%\"></s><s></s><s></s><s style=\"width:80%\"></s><s></s></div><strong>Acme renewal proposal</strong><span>For Priya. Not sent.</span><div class=\"film-btn\">Take a look</div>";
        document.querySelector(`[data-item-id="${id}"]`).parentElement.insertAdjacentElement("afterend", card);
      }, morning);
      stage.pop("#film-proposal", 1650, "left center");
      await stage.settle();
    }
    await tapDot(page, stage, "#film-proposal .film-btn", frame, 1860);
    if (frame === 1860) {
      await page.evaluate(() => {
        const sheet = document.createElement("div");
        sheet.id = "film-sheet";
        sheet.className = "film-sheet";
        sheet.style.translate = "0 100%";
        sheet.innerHTML = "<div class=\"doc film-pending\"><h3>Acme renewal proposal</h3><p>To Priya at Acme</p><h4>Multi-year pricing</h4><table><tr><td>1 year</td><td>$1.2M</td></tr><tr><td>2 years</td><td>$1.14M a year</td></tr><tr><td>3 years</td><td>$1.08M a year</td></tr></table><h4>Seats</h4><p>340 of 400 active. Renews March 14.</p></div><div class=\"film-btn\">Send to Priya</div>";
        document.documentElement.append(sheet);
      });
    }
    if (frame >= 1860 && frame <= 1874) {
      await page.evaluate((t) => { document.getElementById("film-sheet").style.translate = `0 ${(1 - t) * 100}%`; }, smooth((frame - 1860) / 14));
      stage.dirty = true;
    }
    await tapDot(page, stage, "#film-sheet .film-btn", frame, 1950);
    if (frame === 1950) {
      await page.evaluate(() => {
        const sheet = document.getElementById("film-sheet");
        sheet.querySelector(".doc").className = "doc film-done";
        sheet.querySelector(".film-btn").textContent = "Sent to Priya";
        const card = document.getElementById("film-proposal");
        card.className = "film-proposal film-done";
        card.querySelector("span").textContent = "Sent to Priya";
        card.querySelector(".film-btn").remove();
      });
      stage.dirty = true;
    }
    if (frame >= 2025 && frame <= 2039) {
      await page.evaluate((t) => { document.getElementById("film-sheet").style.translate = `0 ${t * 100}%`; }, smooth((frame - 2025) / 14));
      stage.dirty = true;
    }
    if (frame === 2055) await page.getByLabel("Message Nori").evaluate((el) => el.focus());
    await typeInto(page, "Amazing. Turn those numbers into an app for Sam.", 2055, 2114, frame, stage);
    if (frame === 2485) {
      // The app flies into this spot, so measure it before the card appears.
      await page.evaluate((icon) => {
        const log = document.querySelector('[role="log"] > div[aria-live="off"]');
        const card = document.createElement("div");
        card.className = "film-card";
        card.id = "film-dash";
        card.style.visibility = "hidden";
        card.style.margin = "14px 0 0";
        card.innerHTML = `${icon}<span>Renewal tracker.<br>Sam has it.</span>`;
        log.append(card);
      }, APP);
      info.card = await page.evaluate(() => { const r = document.getElementById("film-dash").getBoundingClientRect(); return { x: r.left, y: r.top, w: r.width, h: r.height }; });
      stage.dirty = true;
    }
    if (frame === 2490) {
      await page.evaluate(() => { document.getElementById("film-dash").style.visibility = "visible"; });
      stage.pop("#film-dash", 2490, "left center");
      stage.dirty = true;
    }
    if (frame === 2543) { // Remotion flies a copy of the card off from here
      await page.evaluate(() => { document.getElementById("film-dash").style.visibility = "hidden"; });
      stage.dirty = true;
    }
    await stage.runPops(frame);
    await thinking(page, stage, frame);
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
