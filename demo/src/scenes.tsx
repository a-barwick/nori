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
  [540, 1.35, 832, 580], [570, MORPH_ZOOM, 832, 580], [1275, MORPH_ZOOM, 832, 580],
  [1305, MORPH_ZOOM, (960 - endCamera.tx) / SCALE, (540 - endCamera.ty) / SCALE],
];

const stageCamera = (frame: number) => {
  const keys = camera.map((k) => k[0]);
  const at = (i: number) => interpolate(frame, keys, camera.map((k) => k[i]), { ...clamp, easing: inOut });
  const z = at(1);
  return { z, x: 960 - SCALE * z * at(2), y: 540 - SCALE * z * at(3) };
};

/* ── The background: lavender, the film's purple overnight, then lavender again. ── */

const sky = (frame: number) => {
  const stops = [1440, 1475, 1525, 1560];
  const pick = (colors: string[]) => interpolateColors(frame, stops, colors);
  const top = pick([C.lavender, C.purple, C.purple, C.lavender]);
  const low = pick([C.lavender, "#2a0a7a", "#2a0a7a", C.lavender]);
  return `linear-gradient(180deg, ${top}, ${low})`;
};

const PUSH = 1.5; // close enough to read the thread, with all of it still in frame
const REST = 1.6; // the phone's size when the app's card lands in it
const cardCss = (markers as { phone?: { card: { x: number; y: number; w: number; h: number } } }).phone?.card ?? { x: 18, y: 618, w: 201, h: 70 };
const restY = 540 - (PHONE_C.y + (SCREEN.y + SCALE * (cardCss.y + cardCss.h / 2) - PHONE_C.y) * REST);

/** The phone's move: formed around the screen, still all night, then pushed in and flown through. */
const rig = (frame: number) => {
  const turn = interpolate(frame, [1365, 1383, 1395], [0, -8, -5], { ...clamp, easing: easeOut }) * interpolate(frame, [1620, 1645], [1, 0], clamp);
  const scale = frame < 1305 ? 1 : frame < 2300
    ? interpolate(frame, [1365, 1395, 1620, 1700, 1858, 1878, 2039, 2059], [MORPH_ZOOM, SETTLED, SETTLED, PUSH, PUSH, SETTLED, SETTLED, PUSH], { ...clamp, easing: inOut }) * interpolate(frame, [2160, 2190], [1, 15], { ...clamp, easing: easeIn })
    : interpolate(frame, [2460, 2490], [11, REST], { ...clamp, easing: easeOut });
  const pan = frame < 2300
    ? interpolate(frame, [1620, 1700, 1858, 1878, 2039, 2059, 2140, 2160], [0, 159, 159, 0, 0, -151, -151, -151], { ...clamp, easing: inOut })
    : restY;
  const buzz = (Math.max(0, 1 - Math.abs(frame - 1548) / 4) + Math.max(0, 1 - Math.abs(frame - 1556) / 4)) * Math.sin(frame * 2.6) * 7;
  const drop = interpolate(frame, [2543, 2550], [0, 1400], { ...clamp, easing: easeIn });
  return { turn, scale, buzz, y: pan + drop };
};

const clockTime = (frame: number) => {
  const minutes = interpolate(frame, [1459, 1541], [19 * 60 + 12, 32 * 60], { ...clamp, easing: inOut });
  const hour24 = Math.floor(minutes / 60) % 24;
  const m = Math.floor(minutes % 60);
  const hour = hour24 % 12 || 12;
  return `${hour}:${String(m).padStart(2, "0")} ${hour24 < 12 ? "AM" : "PM"}`;
};

/* ── Scene 6: the phone's lock screen, overnight and at 8:00. ── */

const LockScreen: React.FC = () => {
  const frame = useCurrentFrame();
  const dim = interpolate(frame, [1455, 1487], [0, 1], { ...clamp, easing: inOut });
  const clock = interpolate(frame, [1473, 1485], [0, 1], clamp);
  const lit = interpolate(frame, [1545, 1553], [0, 1], { ...clamp, easing: inOut });
  const gone = interpolate(frame, [1627, 1639], [1, 0], clamp);
  const note = usePop(1549, 12);
  const pops = [1490, 1513, 1536].reduce((sum, at) => sum + Math.max(0, 1 - Math.abs(frame - at) / 7), 0);
  const tap = Math.max(0, 1 - Math.abs(frame - 1620) / 6);
  return (
    <div style={{ position: "absolute", left: SCREEN.x, top: SCREEN.y, width: SCREEN.w, height: SCREEN.h, borderRadius: SCREEN.r, overflow: "hidden", background: `rgba(5,4,14,${dim})`, opacity: gone }}>
      <div style={{ position: "absolute", inset: 0, background: "linear-gradient(180deg, #cdbfff, #eee5fc)", opacity: lit }} />
      <div style={{ position: "absolute", top: 140, left: 0, right: 0, textAlign: "center", opacity: clock, color: lit > 0.5 ? C.ink : "#fff", fontSize: 92, fontWeight: 700, letterSpacing: -3, fontVariantNumeric: "tabular-nums" }}>{clockTime(frame)}</div>
      <div style={{ position: "absolute", bottom: 130, left: 0, right: 0, display: "grid", placeItems: "center", opacity: clock * (1 - lit), scale: String(1 + pops * 0.2) }}><Onigiri size={132} /></div>
      {frame >= 1549 && (
        <div style={{ position: "absolute", left: 22, right: 22, top: 340, scale: String(note), display: "flex", alignItems: "center", gap: 18, padding: "24px 26px", borderRadius: 30, background: "#fffffff2", boxShadow: "0 12px 40px #30106a33", color: C.ink }}>
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
const CLICK = 2430; // Sam's click

const Row: React.FC<{ i: number; selected: boolean }> = ({ i, selected }) => {
  const a = accounts[i];
  const pop = usePop(2295 + i * 15, 12);
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
  const head = usePop(2265, 13);
  const filter = usePop(2280, 12);
  const panel = usePop(2340, 13);
  const line = interpolate(frame, [2343, 2373], [1, 0], { ...clamp, easing: inOut });
  const selected = frame >= CLICK ? 1 : 0;
  const a = accounts[selected];
  const redraw = frame >= CLICK ? interpolate(frame, [CLICK, CLICK + 18], [1, 0], { ...clamp, easing: inOut }) : line;
  return (
    <div style={{ width: 1500, height: 760, borderRadius: 26, background: "#fff", overflow: "hidden", fontFamily }}>
      <div style={{ height: 76, display: "flex", alignItems: "center", gap: 10, padding: "0 24px", borderBottom: "1px solid #ece6f7" }}>
        {["#ff5f57", "#febc2e", "#28c840"].map((c) => <span key={c} style={{ width: 16, height: 16, borderRadius: "50%", background: c }} />)}
      </div>
      <div style={{ padding: "26px 36px", display: frame >= 2265 ? "block" : "none" }}>
        <div style={{ display: "flex", alignItems: "center", justifyContent: "space-between", marginBottom: 20 }}>
          <div style={{ scale: String(head), transformOrigin: "left center", display: "flex", alignItems: "center", gap: 14, fontSize: 46, fontWeight: 800, letterSpacing: -1.4, color: C.ink }}><Onigiri size={58} />Renewal tracker</div>
          <div style={{ scale: String(filter), padding: "12px 24px", borderRadius: 16, border: "2px solid #d8ccf5", fontSize: 28, fontWeight: 650, color: C.ink }}>Renewing soon ▾</div>
        </div>
        <div style={{ display: "grid", gridTemplateColumns: "1.75fr 1fr", gap: 24 }}>
          <div style={{ display: "flex", flexDirection: "column", gap: 14 }}>
            <div style={{ display: "grid", gridTemplateColumns: COLUMNS, padding: "0 26px", fontSize: 24, fontWeight: 600, color: "#6f6585", opacity: frame >= 2295 ? 1 : 0 }}><div>Account</div><div>Value</div><div>Growth</div><div>Seats</div><div>Renews</div></div>
            {accounts.map((_, i) => frame >= 2295 + i * 15 && <Row key={i} i={i} selected={i === selected} />)}
          </div>
          <div style={{ scale: String(panel), padding: "24px 28px", borderRadius: 22, background: "#f6f2ff", display: frame >= 2340 ? "block" : "none" }}>
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
  const t = interpolate(frame, [2400, 2425], [0, 1], { ...clamp, easing: inOut });
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
  const shrink = interpolate(frame, [2460, 2490], [0, 1], { ...clamp, easing: inOut });
  const hit = usePop(2190, 13);
  const end = cardEnd();
  const from = { x: 210, y: 170, w: 1500, h: 760 };
  const x = from.x + (end.x - from.x) * shrink;
  const y = from.y + (end.y - from.y) * shrink;
  const w = from.w + (end.w - from.w) * shrink;
  const h = from.h + (end.h - from.h) * shrink;
  return (
    <AbsoluteFill style={{ fontFamily, pointerEvents: "none" }}>
      {frame < 2461 && (
        <AbsoluteFill style={{ background: `radial-gradient(circle at 30% 0%, ${C.purple}, #2a0a7a 70%)` }}>
          <Ribbons start={2192} muted />
          <div style={{ position: "absolute", left: 0, right: 0, top: 0, height: 46, display: "flex", alignItems: "center", gap: 26, padding: "0 22px", background: "#fdfcffe8", color: C.ink, fontSize: 24, fontWeight: 600 }}>
            <Onigiri size={32} /><span style={{ fontWeight: 800 }}>Nori</span><span>File</span><span>Edit</span><span>View</span><span style={{ marginLeft: "auto" }}>8:04 AM</span>
          </div>
          <div style={{ position: "absolute", left: 660, right: 660, bottom: 18, height: 112, display: "flex", alignItems: "center", justifyContent: "space-around", borderRadius: 30, background: "#fdfcffd0" }}>
            {DOCK.map((c) => <span key={c} style={{ width: 76, height: 76, borderRadius: 19, background: c }} />)}
          </div>
        </AbsoluteFill>
      )}
      <div style={{ position: "absolute", left: x, top: y, width: w, height: h, overflow: "hidden", borderRadius: 26 - 12 * shrink, scale: frame < 2265 ? String(hit) : undefined, background: "#fff", boxShadow: "0 40px 110px #12062e88" }}>
        <div style={{ width: 1500, height: 760, scale: String(w / 1500), transformOrigin: "top left", opacity: 1 - interpolate(shrink, [0.6, 1], [0, 1], clamp) }}>
          <Tracker />
          {frame >= 2400 && <SamCursor />}
        </div>
      </div>
      {frame >= 2193 && frame < 2265 && <Sticker text="Your Nori has its own computer." at={2193} until={2265} size={58} style={{ right: undefined, left: 210, top: 58 }} />}
      {frame >= 2268 && frame < 2395 && <Sticker text="It builds dashboards and apps." at={2268} until={2395} size={58} style={{ right: undefined, left: 210, top: 58 }} />}
    </AbsoluteFill>
  );
};

/* ── The stage: one shot from the wipe at 540 to the fly-off at 2550. ── */

export const Stage: React.FC = () => {
  const frame = useCurrentFrame();
  const wipe = interpolate(frame, [540, 560], [0, 2300], { ...clamp, easing: Easing.bezier(0.5, 0, 0.3, 1) });
  // From 1305 the phone's scale carries the zoom, so the camera sits still at its own zoom 1.
  const cam = frame < 1305 ? stageCamera(frame) : { z: 1, x: endCamera.tx, y: endCamera.ty };
  const r = rig(frame);
  const i = Math.max(0, Math.min(morph.length - 1, frame - 1305));
  const w = morph[i];
  const mask = frame >= 1305 ? { x: endCamera.tx + SCALE * w.x, y: endCamera.ty + SCALE * w.y, w: SCALE * w.w, h: SCALE * w.h, r: SCALE * w.r } : null;
  const body = interpolate(frame, [1365, 1395], [0, 1], { ...clamp, easing: easeOut });
  const sameThread = interpolate(frame, [1395, 1421], [0, 1], { ...clamp, easing: inOut }) * interpolate(frame, [1445, 1463], [1, 0], clamp);
  const sameRise = interpolate(frame, [1395, 1425], [22, 0], { ...clamp, easing: easeOut });
  const transform = `translate(${r.buzz}px, ${r.y}px) perspective(2400px) rotateY(${r.turn}deg) scale(${r.scale})`;
  const phoneHidden = frame >= 2190 && frame < 2461;
  return (
    <AbsoluteFill style={{ clipPath: frame < 560 ? `circle(${wipe}px at 1325px 510px)` : undefined, background: frame < 1305 ? C.lavender : sky(frame), fontFamily, overflow: "hidden" }}>
      {!phoneHidden && (
        <AbsoluteFill style={{ transform, transformOrigin: `${PHONE_C.x}px ${PHONE_C.y}px`, transformStyle: "preserve-3d" }}>
          {body > 0 && <div style={{ position: "absolute", left: SCREEN.x - 15, top: SCREEN.y - 15, width: SCREEN.w + 30, height: SCREEN.h + 30, borderRadius: SCREEN.r + 15, background: "#08060f", boxShadow: "0 50px 120px #000a, 0 0 0 2px #453a6b", opacity: body, scale: String(1.035 - 0.035 * body) }} />}
          {frame < 1491 && (
            <div style={{ position: "absolute", inset: 0, clipPath: mask ? `inset(${mask.y}px ${1920 - mask.x - mask.w}px ${1080 - mask.y - mask.h}px ${mask.x}px round ${mask.r}px)` : undefined }}>
              <div style={{ position: "absolute", left: cam.x, top: cam.y, width: 1920 * cam.z, height: 1920 * cam.z * (1200 / 1664) }}>
                <Sequence from={540} durationInFrames={951} layout="none"><Video src={staticFile("clips/desktop.mp4")} muted style={{ width: "100%", height: "100%", display: "block" }} /></Sequence>
              </div>
            </div>
          )}
          {frame >= 1485 && (
            <div style={{ position: "absolute", left: SCREEN.x, top: SCREEN.y, width: SCREEN.w, height: SCREEN.h, borderRadius: SCREEN.r, overflow: "hidden", background: "#fff" }}>
              <Sequence from={1485} durationInFrames={1065} layout="none"><Video src={staticFile("clips/phone.mp4")} muted style={{ width: "100%", height: "100%", display: "block" }} /></Sequence>
            </div>
          )}
          {frame >= 1455 && frame < 1639 && <LockScreen />}
        </AbsoluteFill>
      )}
      {frame >= 765 && frame < 825 && <Sticker text="All in one thread." at={765} until={825} style={{ top: undefined, right: 150, bottom: 245 }} />}
      {sameThread > 0 && <div style={{ position: "absolute", left: 56, top: 470, translate: `0 ${sameRise}px`, opacity: sameThread, color: C.purple, fontSize: 108, fontWeight: 800, letterSpacing: -4 }}>Same thread.</div>}
      {frame >= 1770 && frame < 1845 && <Sticker text="You didn't ask." at={1770} until={1845} size={78} style={{ right: undefined, left: 30, top: 470 }} />}
      {frame >= 1950 && frame < 2025 && <Sticker text={"Nothing goes out\nuntil you tap."} at={1950} until={2025} size={62} style={{ right: undefined, left: 80, top: 440 }} />}
      {frame >= 2190 && frame < 2491 && <Computer />}
    </AbsoluteFill>
  );
};

/** The card leaves the phone and flies off as the trust cards land. */
export const CardFlyOff: React.FC = () => {
  const frame = useCurrentFrame();
  const t = interpolate(frame, [2543, 2563], [0, 1], { ...clamp, easing: easeIn });
  const end = cardEnd();
  if (frame < 2543 || frame >= 2563) return null;
  return (
    <div style={{ position: "absolute", left: end.x, top: end.y, width: end.w, height: end.h, translate: `${t * 1500}px ${-t * 900}px`, rotate: `${t * 28}deg`, scale: String(1 + t * 0.7), borderRadius: 16, background: "#fff", border: "1px solid #d4c9e9", display: "flex", alignItems: "center", gap: 14, padding: "0 18px", fontFamily, fontSize: 28, lineHeight: 1.2, fontWeight: 600, color: C.ink, boxShadow: "0 20px 50px #0003", whiteSpace: "nowrap", overflow: "hidden" }}>
      <span style={{ width: 64, height: 64, borderRadius: 16, background: C.lavender, flexShrink: 0, display: "grid", placeItems: "center" }}>
        <svg width={38} height={38} viewBox="0 0 24 24"><path d="M3 5h18M3 12h18M3 19h18" stroke={C.purple} strokeWidth={3.4} strokeLinecap="round" /></svg>
      </span>
      <span>Renewal tracker.<br />Sam has it.</span>
    </div>
  );
};

/* ── End: the deck's closing line, ribbons, and a wave from the mark. ── */

export const End: React.FC = () => {
  const frame = useCurrentFrame();
  const one = usePop(0, 14);
  const two = usePop(45, 12);
  const mark = usePop(120, 10);
  const name = usePop(120, 14);
  const blink = interpolate(frame, [165, 168, 171], [0, 1, 0], clamp);
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
