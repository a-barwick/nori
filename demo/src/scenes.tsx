import { Video } from "@remotion/media";
import React from "react";
import { AbsoluteFill, Easing, Sequence, interpolate, interpolateColors, staticFile, useCurrentFrame } from "remotion";
import markers from "../public/clips/markers.json";
import { C, Onigiri, Ribbons, Sticker, clamp, easeOut, fontFamily, usePop } from "./kit";

const SCALE = 1920 / 1664; // CSS pixels of the filmed page to film pixels at camera zoom 1
const inOut = Easing.bezier(0.45, 0, 0.2, 1);
const easeIn = Easing.bezier(0.6, 0, 0.9, 0.4);
const { camera: endCamera, window: morph } = markers.desktop;
const phoneCss = morph[morph.length - 1];
/** Where the phone's screen sits in the frame, set by the Acme message holding still. */
const SCREEN = { x: endCamera.tx + SCALE * phoneCss.x, y: endCamera.ty + SCALE * phoneCss.y, w: SCALE * phoneCss.w, h: SCALE * phoneCss.h, r: SCALE * phoneCss.r };
const PHONE_C = { x: 960, y: 540 }; // the Acme message's centre, which the phone scales around
const MORPH_ZOOM = 1.5; // keeps the Acme text large while the desktop becomes the phone
const SETTLED = 1.05; // the whole phone in frame

/* ── Scenes 3 and 4: the real interface, with a camera and sticker captions. ── */

// [film frame, zoom, the page point (CSS pixels) held at frame centre]
const camera: [number, number, number, number][] = [
  [480, 1, 832, 468], [510, 1.5, 832, 660], [525, 1.85, 832, 372], [1020, 1.85, 832, 372], [1040, 1.85, 832, 500], [1265, 1.85, 832, 500], [1285, 1.85, 832, 540], [1410, 1.85, 832, 540],
  [1440, MORPH_ZOOM, (960 - endCamera.tx) / SCALE, (540 - endCamera.ty) / SCALE],
];

const stageCamera = (frame: number) => {
  const keys = camera.map((k) => k[0]);
  const at = (i: number) => interpolate(frame, keys, camera.map((k) => k[i]), { ...clamp, easing: inOut });
  const z = at(1);
  return { z, x: 960 - SCALE * z * at(2), y: 540 - SCALE * z * at(3) };
};

/* ── The background, from lavender through evening, night and sunrise. ── */

const sky = (frame: number) => {
  const stops = [1440, 1530, 1590, 1610, 1680, 1710];
  const pick = (colors: string[]) => interpolateColors(frame, stops, colors);
  const top = pick([C.lavender, "#241033", "#241033", "#0a0720", "#ff9e7a", "#ffc7a0"]);
  const low = pick([C.lavender, "#a04e2e", "#a04e2e", "#0d0a26", "#ffd9a8", "#ffeccf"]);
  return `linear-gradient(180deg, ${top}, ${low})`;
};

const PUSH = 1.5; // close enough to read the thread, with all of it still in frame
const REST = 1.6; // the phone's size when the app's card lands in it
const cardCss = (markers as { phone?: { card: { x: number; y: number; w: number; h: number } } }).phone?.card ?? { x: 18, y: 618, w: 201, h: 70 };
const restY = 540 - (PHONE_C.y + (SCREEN.y + SCALE * (cardCss.y + cardCss.h / 2) - PHONE_C.y) * REST);

/** The phone's move: formed around the screen, still all night, then pushed in and flown through. */
const rig = (frame: number) => {
  const turn = interpolate(frame, [1500, 1518, 1530], [0, -8, -5], { ...clamp, easing: easeOut }) * interpolate(frame, [1755, 1780], [1, 0], clamp);
  const scale = frame < 1440 ? 1 : frame < 2600
    ? interpolate(frame, [1500, 1530, 1755, 1835, 2215, 2235, 2354, 2374], [MORPH_ZOOM, SETTLED, SETTLED, PUSH, PUSH, SETTLED, SETTLED, PUSH], { ...clamp, easing: inOut }) * interpolate(frame, [2490, 2520], [1, 15], { ...clamp, easing: easeIn })
    : interpolate(frame, [2805, 2835], [11, REST], { ...clamp, easing: easeOut });
  const pan = frame < 2600
    ? interpolate(frame, [1755, 1835, 2215, 2235, 2354, 2374, 2470, 2490], [0, 159, 159, 0, 0, -151, -151, -151], { ...clamp, easing: inOut })
    : restY;
  const buzz = (Math.max(0, 1 - Math.abs(frame - 1683) / 4) + Math.max(0, 1 - Math.abs(frame - 1691) / 4)) * Math.sin(frame * 2.6) * 7;
  const drop = interpolate(frame, [2903, 2910], [0, 1400], { ...clamp, easing: easeIn });
  return { turn, scale, buzz, y: pan + drop };
};

const clockTime = (frame: number) => {
  const minutes = interpolate(frame, [1594, 1676], [19 * 60 + 12, 32 * 60], { ...clamp, easing: inOut });
  const hour24 = Math.floor(minutes / 60) % 24;
  const m = Math.floor(minutes % 60);
  const hour = hour24 % 12 || 12;
  return `${hour}:${String(m).padStart(2, "0")} ${hour24 < 12 ? "AM" : "PM"}`;
};

/* ── Scene 6: the phone's lock screen, overnight and at 8:00. ── */

const LockScreen: React.FC = () => {
  const frame = useCurrentFrame();
  const dim = interpolate(frame, [1590, 1622], [0, 1], { ...clamp, easing: inOut });
  const clock = interpolate(frame, [1608, 1620], [0, 1], clamp);
  const lit = interpolate(frame, [1680, 1688], [0, 1], { ...clamp, easing: inOut });
  const gone = interpolate(frame, [1762, 1774], [1, 0], clamp);
  const note = usePop(1684, 12);
  const pops = [1625, 1648, 1671].reduce((sum, at) => sum + Math.max(0, 1 - Math.abs(frame - at) / 7), 0);
  const tap = Math.max(0, 1 - Math.abs(frame - 1755) / 6);
  return (
    <div style={{ position: "absolute", left: SCREEN.x, top: SCREEN.y, width: SCREEN.w, height: SCREEN.h, borderRadius: SCREEN.r, overflow: "hidden", background: `rgba(5,4,14,${dim})`, opacity: gone }}>
      <div style={{ position: "absolute", inset: 0, background: "linear-gradient(180deg, #ff9e7a, #ffd9a8)", opacity: lit }} />
      <div style={{ position: "absolute", top: 140, left: 0, right: 0, textAlign: "center", opacity: clock, color: lit > 0.5 ? C.ink : "#fff", fontSize: 92, fontWeight: 700, letterSpacing: -3, fontVariantNumeric: "tabular-nums" }}>{clockTime(frame)}</div>
      <div style={{ position: "absolute", bottom: 130, left: 0, right: 0, display: "grid", placeItems: "center", opacity: clock * (1 - lit), scale: String(1 + pops * 0.2) }}><Onigiri size={132} /></div>
      {frame >= 1684 && (
        <div style={{ position: "absolute", left: 22, right: 22, top: 340, scale: String(note), display: "flex", alignItems: "center", gap: 18, padding: "24px 26px", borderRadius: 30, background: "#fffffff2", boxShadow: "0 12px 40px #6b2a1033", color: C.ink }}>
          <Onigiri size={72} />
          <div><div style={{ fontSize: 26, fontWeight: 700 }}>Nori</div><div style={{ fontSize: 28, lineHeight: 1.2 }}>Morning! Hope dinner went well. 🍣</div></div>
        </div>
      )}
      {tap > 0 && <div style={{ position: "absolute", left: SCREEN.w / 2 - 40, top: 400, width: 80, height: 80, borderRadius: "50%", background: "#19093640", opacity: tap }} />}
    </div>
  );
};

/* ── Scene 7: her own computer, with a working app built on the beat. ── */

const accounts = [
  { name: "Acme", value: "$1.2M", growth: "+18%", seats: "340/400", renews: "March 14", line: "M 0 170 C 90 160, 150 130, 240 112 S 400 80, 480 58 S 620 24, 700 10" },
  { name: "Globex", value: "$640K", growth: "+6%", seats: "210/260", renews: "April 2", line: "M 0 120 C 90 124, 150 96, 240 104 S 400 88, 480 76 S 620 72, 700 60" },
  { name: "Initech", value: "$310K", growth: "-4%", seats: "90/150", renews: "May 9", line: "M 0 60 C 90 66, 150 92, 240 84 S 400 104, 480 118 S 620 112, 700 128" },
];
const COLUMNS = "1.1fr 0.9fr 0.8fr 0.9fr 1fr";
const CLICK = 2775; // Sam's click

const Row: React.FC<{ i: number; selected: boolean }> = ({ i, selected }) => {
  const a = accounts[i];
  const pop = usePop(2640 + i * 15, 12);
  return (
    <div style={{ scale: String(pop), display: "grid", gridTemplateColumns: COLUMNS, alignItems: "center", height: 104, padding: "0 24px", borderRadius: 20, background: selected ? C.lavender : "#fff", border: `2px solid ${selected ? C.purple : "#ece6f7"}`, fontSize: 32, fontWeight: 600, color: C.ink }}>
      <div style={{ fontWeight: 800 }}>{a.name}</div>
      <div>{a.value}</div>
      <div style={{ color: a.growth[0] === "+" ? "#1a8a57" : C.coral, fontWeight: 700 }}>{a.growth}</div>
      <div>{a.seats}</div>
      <div>{a.renews}</div>
    </div>
  );
};

/** A working renewal tracker: a filter, rows, and a detail chart that follows the selected row. */
const Tracker: React.FC = () => {
  const frame = useCurrentFrame();
  const head = usePop(2610, 13);
  const filter = usePop(2625, 12);
  const panel = usePop(2685, 13);
  const line = interpolate(frame, [2688, 2718], [1, 0], { ...clamp, easing: inOut });
  const selected = frame >= CLICK ? 1 : 0;
  const a = accounts[selected];
  const redraw = frame >= CLICK ? interpolate(frame, [CLICK, CLICK + 18], [1, 0], { ...clamp, easing: inOut }) : line;
  return (
    <div style={{ width: 1500, height: 760, borderRadius: 26, background: "#fff", overflow: "hidden", fontFamily }}>
      <div style={{ height: 76, display: "flex", alignItems: "center", gap: 10, padding: "0 24px", borderBottom: "1px solid #ece6f7" }}>
        {["#ff5f57", "#febc2e", "#28c840"].map((c) => <span key={c} style={{ width: 16, height: 16, borderRadius: "50%", background: c }} />)}
      </div>
      <div style={{ padding: "26px 36px", display: frame >= 2610 ? "block" : "none" }}>
        <div style={{ display: "flex", alignItems: "center", justifyContent: "space-between", marginBottom: 20 }}>
          <div style={{ scale: String(head), transformOrigin: "left center", display: "flex", alignItems: "center", gap: 14, fontSize: 46, fontWeight: 800, letterSpacing: -1.4, color: C.ink }}><Onigiri size={58} />Renewal tracker</div>
          <div style={{ scale: String(filter), padding: "12px 24px", borderRadius: 16, border: "2px solid #d8ccf5", fontSize: 28, fontWeight: 650, color: C.ink }}>Renewing in Q1 ▾</div>
        </div>
        <div style={{ display: "grid", gridTemplateColumns: "1.75fr 1fr", gap: 24 }}>
          <div style={{ display: "flex", flexDirection: "column", gap: 14 }}>
            <div style={{ display: "grid", gridTemplateColumns: COLUMNS, padding: "0 26px", fontSize: 24, fontWeight: 600, color: "#6f6585", opacity: frame >= 2640 ? 1 : 0 }}><div>Account</div><div>Value</div><div>Growth</div><div>Seats</div><div>Renews</div></div>
            {accounts.map((_, i) => frame >= 2640 + i * 15 && <Row key={i} i={i} selected={i === selected} />)}
          </div>
          <div style={{ scale: String(panel), padding: "24px 28px", borderRadius: 22, background: "#f6f2ff", display: frame >= 2685 ? "block" : "none" }}>
            <div style={{ fontSize: 30, fontWeight: 700, color: C.ink }}>{a.name}</div>
            <div style={{ fontSize: 72, fontWeight: 850, letterSpacing: -2.5, color: C.purple, lineHeight: 1.1 }}>{a.value}</div>
            <svg width="100%" height={200} viewBox="0 0 700 180" preserveAspectRatio="none" style={{ overflow: "visible", marginTop: 10 }}>
              <path d={a.line} fill="none" stroke={C.purple} strokeWidth={8} strokeLinecap="round" pathLength={1} strokeDasharray={1} strokeDashoffset={redraw} vectorEffect="non-scaling-stroke" />
            </svg>
          </div>
        </div>
      </div>
    </div>
  );
};

/** Sam's cursor arrives in the shared app and clicks a row, in the window's own pixels. */
const SamCursor: React.FC = () => {
  const frame = useCurrentFrame();
  const t = interpolate(frame, [2745, 2770], [0, 1], { ...clamp, easing: inOut });
  const x = 1420 + (826 - 1420) * t;
  const y = 730 + (385 - 730) * t;
  const press = frame >= CLICK - 3 && frame < CLICK + 3 ? 0.84 : 1;
  return (
    <div style={{ position: "absolute", left: x, top: y, scale: String(press), transformOrigin: "6px 4px", display: "flex", alignItems: "flex-start" }}>
      <svg width={46} height={46} viewBox="0 0 24 24"><path d="M5 3l14 8-6.5 1.6L9.5 19z" fill={C.coral} stroke="#fff" strokeWidth={1.6} strokeLinejoin="round" /></svg>
      <div style={{ marginTop: 30, marginLeft: -8, padding: "4px 14px", borderRadius: 12, background: C.coral, color: "#fff", fontSize: 26, fontWeight: 700 }}>Sam</div>
    </div>
  );
};

/** The card's place in the frame once the phone has pulled back to its resting size. */
const cardEnd = () => {
  const s = SCALE * REST;
  return { x: PHONE_C.x + (cardCss.x * SCALE + SCREEN.x - PHONE_C.x) * REST, y: restY + PHONE_C.y + (cardCss.y * SCALE + SCREEN.y - PHONE_C.y) * REST, w: cardCss.w * s, h: cardCss.h * s };
};

const DOCK = ["#5000ff", "#ff4b62", "#1a8f8a", "#d9822b", "#3f7cf0"];

const Computer: React.FC = () => {
  const frame = useCurrentFrame();
  const shrink = interpolate(frame, [2805, 2835], [0, 1], { ...clamp, easing: inOut });
  const hit = usePop(2520, 13);
  const end = cardEnd();
  const from = { x: 210, y: 170, w: 1500, h: 760 };
  const x = from.x + (end.x - from.x) * shrink;
  const y = from.y + (end.y - from.y) * shrink;
  const w = from.w + (end.w - from.w) * shrink;
  const h = from.h + (end.h - from.h) * shrink;
  return (
    <AbsoluteFill style={{ fontFamily, pointerEvents: "none" }}>
      {frame < 2806 && (
        <AbsoluteFill style={{ background: `radial-gradient(circle at 30% 0%, ${C.purple}, #2a0a7a 70%)` }}>
          <Ribbons start={2522} muted />
          <div style={{ position: "absolute", left: 0, right: 0, top: 0, height: 46, display: "flex", alignItems: "center", gap: 26, padding: "0 22px", background: "#fdfcffe8", color: C.ink, fontSize: 24, fontWeight: 600 }}>
            <Onigiri size={32} /><span style={{ fontWeight: 800 }}>Nori</span><span>File</span><span>Edit</span><span>View</span><span style={{ marginLeft: "auto" }}>8:04 AM</span>
          </div>
          <div style={{ position: "absolute", left: 660, right: 660, bottom: 18, height: 112, display: "flex", alignItems: "center", justifyContent: "space-around", borderRadius: 30, background: "#fdfcffd0" }}>
            {DOCK.map((c) => <span key={c} style={{ width: 76, height: 76, borderRadius: 19, background: c }} />)}
          </div>
        </AbsoluteFill>
      )}
      <div style={{ position: "absolute", left: x, top: y, width: w, height: h, overflow: "hidden", borderRadius: 26 - 12 * shrink, scale: frame < 2610 ? String(hit) : undefined, background: "#fff", boxShadow: "0 40px 110px #12062e88" }}>
        <div style={{ width: 1500, height: 760, scale: String(w / 1500), transformOrigin: "top left", opacity: 1 - interpolate(shrink, [0.6, 1], [0, 1], clamp) }}>
          <Tracker />
          {frame >= 2745 && <SamCursor />}
        </div>
      </div>
      {frame >= 2523 && frame < 2610 && <Sticker text="Your Nori has its own computer." at={2523} until={2610} size={58} style={{ right: undefined, left: 210, top: 58 }} />}
      {frame >= 2613 && frame < 2740 && <Sticker text="It builds dashboards and apps." at={2613} until={2740} size={58} style={{ right: undefined, left: 210, top: 58 }} />}
    </AbsoluteFill>
  );
};

/* ── The stage: one shot from the wipe at 480 to the fly-off at 2910. ── */

export const Stage: React.FC = () => {
  const frame = useCurrentFrame();
  const wipe = interpolate(frame, [480, 500], [0, 2300], { ...clamp, easing: Easing.bezier(0.5, 0, 0.3, 1) });
  // From 1440 the phone's scale carries the zoom, so the camera sits still at its own zoom 1.
  const cam = frame < 1440 ? stageCamera(frame) : { z: 1, x: endCamera.tx, y: endCamera.ty };
  const r = rig(frame);
  const i = Math.max(0, Math.min(morph.length - 1, frame - 1440));
  const w = morph[i];
  const mask = frame >= 1440 ? { x: endCamera.tx + SCALE * w.x, y: endCamera.ty + SCALE * w.y, w: SCALE * w.w, h: SCALE * w.h, r: SCALE * w.r } : null;
  const body = interpolate(frame, [1500, 1530], [0, 1], { ...clamp, easing: easeOut });
  const sameThread = interpolate(frame, [1530, 1556], [0, 1], { ...clamp, easing: inOut }) * interpolate(frame, [1590, 1608], [1, 0], clamp);
  const sameRise = interpolate(frame, [1530, 1560], [22, 0], { ...clamp, easing: easeOut });
  const transform = `translate(${r.buzz}px, ${r.y}px) perspective(2400px) rotateY(${r.turn}deg) scale(${r.scale})`;
  const phoneHidden = frame >= 2520 && frame < 2806;
  return (
    <AbsoluteFill style={{ clipPath: frame < 500 ? `circle(${wipe}px at 1325px 510px)` : undefined, background: frame < 1440 ? C.lavender : sky(frame), fontFamily, overflow: "hidden" }}>
      {!phoneHidden && (
        <AbsoluteFill style={{ transform, transformOrigin: `${PHONE_C.x}px ${PHONE_C.y}px`, transformStyle: "preserve-3d" }}>
          {body > 0 && <div style={{ position: "absolute", left: SCREEN.x - 15, top: SCREEN.y - 15, width: SCREEN.w + 30, height: SCREEN.h + 30, borderRadius: SCREEN.r + 15, background: "#08060f", boxShadow: "0 50px 120px #000a, 0 0 0 2px #453a6b", opacity: body, scale: String(1.035 - 0.035 * body) }} />}
          {frame < 1626 && (
            <div style={{ position: "absolute", inset: 0, clipPath: mask ? `inset(${mask.y}px ${1920 - mask.x - mask.w}px ${1080 - mask.y - mask.h}px ${mask.x}px round ${mask.r}px)` : undefined }}>
              <div style={{ position: "absolute", left: cam.x, top: cam.y, width: 1920 * cam.z, height: 1920 * cam.z * (1200 / 1664) }}>
                <Sequence from={480} durationInFrames={1146} layout="none"><Video src={staticFile("clips/desktop.mp4")} muted style={{ width: "100%", height: "100%", display: "block" }} /></Sequence>
              </div>
            </div>
          )}
          {frame >= 1620 && (
            <div style={{ position: "absolute", left: SCREEN.x, top: SCREEN.y, width: SCREEN.w, height: SCREEN.h, borderRadius: SCREEN.r, overflow: "hidden", background: "#fff" }}>
              <Sequence from={1620} durationInFrames={1290} layout="none"><Video src={staticFile("clips/phone.mp4")} muted style={{ width: "100%", height: "100%", display: "block" }} /></Sequence>
            </div>
          )}
          {frame >= 1590 && frame < 1774 && <LockScreen />}
        </AbsoluteFill>
      )}
      {frame >= 795 && frame < 885 && <Sticker text="One thread for everything." at={795} until={885} style={{ top: undefined, right: 64, bottom: 72 }} />}
      {sameThread > 0 && <div style={{ position: "absolute", left: 56, top: 470, translate: `0 ${sameRise}px`, opacity: sameThread, color: "#fff", fontSize: 108, fontWeight: 800, letterSpacing: -4 }}>Same thread.</div>}
      {frame >= 2145 && frame < 2220 && <Sticker text="You didn't ask." at={2145} until={2220} size={78} style={{ right: undefined, left: 30, top: 470 }} />}
      {frame >= 2520 && frame < 2836 && <Computer />}
    </AbsoluteFill>
  );
};

/** The card leaves the phone and flies off as the trust cards land. */
export const CardFlyOff: React.FC = () => {
  const frame = useCurrentFrame();
  const t = interpolate(frame, [2903, 2923], [0, 1], { ...clamp, easing: easeIn });
  const end = cardEnd();
  if (frame < 2903 || frame >= 2923) return null;
  return (
    <div style={{ position: "absolute", left: end.x, top: end.y, width: end.w, height: end.h, translate: `${t * 1500}px ${-t * 900}px`, rotate: `${t * 28}deg`, scale: String(1 + t * 0.7), borderRadius: 16, background: "#fff", border: "1px solid #d4c9e9", display: "flex", alignItems: "center", gap: 14, padding: "0 18px", fontFamily, fontSize: 28, lineHeight: 1.2, fontWeight: 600, color: C.ink, boxShadow: "0 20px 50px #0003", whiteSpace: "nowrap", overflow: "hidden" }}>
      <span style={{ width: 64, height: 64, borderRadius: 16, background: C.lavender, flexShrink: 0, display: "grid", placeItems: "center" }}>
        <svg width={38} height={38} viewBox="0 0 24 24"><path d="M3 5h18M3 12h18M3 19h18" stroke={C.purple} strokeWidth={3.4} strokeLinecap="round" /></svg>
      </span>
      <span>Renewal tracker.<br />Shared with Sam.</span>
    </div>
  );
};

/* ── Trust: the premise, in three titles. ── */

const trust = ["Uses your permissions", "Proposes on its own", "Writes when you say so"];

const TrustCard: React.FC<{ title: string; at: number; i: number }> = ({ title, at, i }) => {
  const pop = usePop(at, 12);
  const accent = i === 2;
  return (
    <div style={{ scale: String(pop), borderRadius: 34, padding: "40px 46px", background: accent ? C.purple : "#f1ebff", color: accent ? "#fff" : C.ink, display: "flex", alignItems: "center" }}>
      <div style={{ fontSize: 84, fontWeight: 850, letterSpacing: -2.6, lineHeight: 1.04 }}>{title}</div>
    </div>
  );
};

export const Trust: React.FC = () => (
  <AbsoluteFill style={{ background: "#fff", fontFamily }}>
    <div style={{ position: "absolute", left: 64, right: 64, top: 240, bottom: 240, display: "grid", gridTemplateColumns: "1fr 1fr 1fr", gap: 26 }}>
      {trust.map((title, i) => <TrustCard key={title} title={title} at={i * 30} i={i} />)}
    </div>
  </AbsoluteFill>
);

/* ── End: the deck's closing line, ribbons, and a wave from the mark. ── */

export const End: React.FC = () => {
  const frame = useCurrentFrame();
  const one = usePop(0, 14);
  const two = usePop(45, 12);
  const mark = usePop(120, 10);
  const name = usePop(120, 14);
  const blink = interpolate(frame, [190, 193, 196], [0, 1, 0], clamp);
  return (
    <AbsoluteFill style={{ background: "#fff", fontFamily }}>
      <Ribbons start={45} dur={40} />
      <div style={{ position: "absolute", left: 120, top: 230, fontSize: 124, fontWeight: 800, letterSpacing: -4.5, lineHeight: 1.1 }}>
        <div style={{ color: C.ink, scale: String(one), transformOrigin: "left center" }}>We delivered chat.</div>
        <div style={{ color: C.purple, scale: String(two), transformOrigin: "left center" }}>Now let's deliver<br />an assistant.</div>
      </div>
      <div style={{ position: "absolute", left: 120, top: 700, display: "flex", alignItems: "center", gap: 26 }}>
        <div style={{ scale: String(mark) }}><Onigiri size={92} happy={frame > 124 && frame < 170 ? 1 : 0} blink={blink} /></div>
        <div style={{ fontSize: 56, fontWeight: 700, color: C.ink, lineHeight: 1.15, scale: String(name), transformOrigin: "left center" }}>Nori, inside Nova.</div>
      </div>
    </AbsoluteFill>
  );
};
