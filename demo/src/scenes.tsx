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
  [300, 1, 832, 468], [330, 1.5, 832, 660], [345, 1.85, 832, 372], [535, 1.85, 832, 372], [552, 1.3, 860, 420], [630, 1.3, 860, 420],
  [648, 1.85, 832, 410], [710, 1.85, 832, 410], [728, 1.85, 832, 510], [780, 1.85, 832, 510], [810, MORPH_ZOOM, (960 - endCamera.tx) / SCALE, (540 - endCamera.ty) / SCALE],
];

const stageCamera = (frame: number) => {
  const keys = camera.map((k) => k[0]);
  const at = (i: number) => interpolate(frame, keys, camera.map((k) => k[i]), { ...clamp, easing: inOut });
  const z = at(1);
  return { z, x: 960 - SCALE * z * at(2), y: 540 - SCALE * z * at(3) };
};

const stickers: [number, number, string][] = [
  [450, 480, "Three asks. One thread."], [483, 540, "Quick answers come right back."], [543, 630, "The rest keep running."],
  [633, 720, "The whole team's invited."], [723, 780, "Done. All in one thread."],
];

/* ── The background, from lavender through evening, night and sunrise. ── */

const sky = (frame: number) => {
  const stops = [810, 900, 930, 950, 990, 1020];
  const pick = (colors: string[]) => interpolateColors(frame, stops, colors);
  const top = pick([C.lavender, "#241033", "#241033", "#0a0720", "#ff9e7a", "#ffc7a0"]);
  const low = pick([C.lavender, "#a04e2e", "#a04e2e", "#0d0a26", "#ffd9a8", "#ffeccf"]);
  return `linear-gradient(180deg, ${top}, ${low})`;
};

const PUSH = 1.55;
const REST = 1.6; // the phone's size when the dashboard card lands in it
const cardCss = (markers as { phone?: { card: { x: number; y: number; w: number; h: number } } }).phone?.card ?? { x: 18, y: 600, w: 354, h: 62 };
const restY = 540 - (PHONE_C.y + (SCREEN.y + SCALE * (cardCss.y + cardCss.h / 2) - PHONE_C.y) * REST);

/** The phone's move: formed around the screen, still all night, then pushed in and flown through. */
const rig = (frame: number) => {
  const turn = interpolate(frame, [870, 888, 900], [0, -8, -5], { ...clamp, easing: easeOut }) * interpolate(frame, [1095, 1120], [1, 0], clamp);
  const scale = frame < 810 ? 1 : frame < 1300
    ? interpolate(frame, [870, 900, 990, 1110], [MORPH_ZOOM, SETTLED, SETTLED, PUSH], { ...clamp, easing: inOut }) * interpolate(frame, [1230, 1260], [1, 15], { ...clamp, easing: easeIn })
    : interpolate(frame, [1380, 1410], [11, REST], { ...clamp, easing: easeOut });
  const pan = frame < 1300
    ? interpolate(frame, [990, 1110, 1188, 1224], [0, 215, 215, -190], { ...clamp, easing: inOut })
    : restY;
  const buzz = (Math.max(0, 1 - Math.abs(frame - 993) / 4) + Math.max(0, 1 - Math.abs(frame - 1001) / 4)) * Math.sin(frame * 2.6) * 7;
  const drop = interpolate(frame, [1463, 1470], [0, 1400], { ...clamp, easing: easeIn });
  return { turn, scale, buzz, y: pan + drop };
};

const clockTime = (frame: number) => {
  const minutes = interpolate(frame, [934, 986], [19 * 60 + 12, 32 * 60], { ...clamp, easing: inOut });
  const hour24 = Math.floor(minutes / 60) % 24;
  const m = Math.floor(minutes % 60);
  const hour = hour24 % 12 || 12;
  return `${hour}:${String(m).padStart(2, "0")} ${hour24 < 12 ? "AM" : "PM"}`;
};

/* ── Scene 7: her own computer, with a dashboard built on the beat. ── */

const Tile: React.FC<{ at: number; label: string; children: React.ReactNode; style?: React.CSSProperties }> = ({ at, label, children, style }) => {
  const pop = usePop(at, 11);
  return (
    <div style={{ scale: String(pop), borderRadius: 26, padding: "26px 32px", background: "#f6f2ff", color: C.ink, display: "flex", flexDirection: "column", justifyContent: "space-between", ...style }}>
      <div style={{ fontSize: 30, fontWeight: 600, opacity: 0.66 }}>{label}</div>
      {children}
    </div>
  );
};

const Big: React.FC<{ children: React.ReactNode }> = ({ children }) => <div style={{ fontSize: 88, fontWeight: 850, letterSpacing: -3, color: C.purple, lineHeight: 1 }}>{children}</div>;

const CHART = "M 0 200 C 60 190, 110 176, 170 168 S 280 150, 340 132 S 450 124, 510 92 S 620 74, 680 56 S 790 34, 850 18 S 960 14, 1010 6";

const Dashboard: React.FC = () => {
  const frame = useCurrentFrame();
  const line = interpolate(frame, [1350, 1380], [1, 0], { ...clamp, easing: inOut });
  const seats = interpolate(frame, [1320, 1340], [0, 340 / 400], { ...clamp, easing: easeOut });
  const r = 58;
  return (
    <div style={{ width: 1560, height: 880, borderRadius: 26, background: "#fff", boxShadow: "0 40px 110px #12062e88", overflow: "hidden", fontFamily }}>
      <div style={{ height: 84, display: "flex", alignItems: "center", gap: 14, padding: "0 28px", borderBottom: "1px solid #ece6f7" }}>
        <Onigiri size={52} />
        <div style={{ fontSize: 32, fontWeight: 750, color: C.ink, letterSpacing: -0.6 }}>Acme renewal</div>
      </div>
      <div style={{ padding: 28, display: "grid", gridTemplateColumns: "repeat(4, 1fr)", gridTemplateRows: "230px 1fr", gap: 22, height: 796 }}>
        <Tile at={1290} label="Revenue"><Big>$1.2M</Big></Tile>
        <Tile at={1305} label="Growth"><Big>+18%</Big></Tile>
        <Tile at={1320} label="Seats">
          <div style={{ display: "flex", alignItems: "center", justifyContent: "space-between" }}>
            <div style={{ fontSize: 58, fontWeight: 850, letterSpacing: -2, color: C.purple }}>340/400</div>
            <svg width={120} height={120} viewBox="0 0 140 140" style={{ rotate: "-90deg" }}>
              <circle cx={70} cy={70} r={r} fill="none" stroke="#e2d8fb" strokeWidth={20} />
              <circle cx={70} cy={70} r={r} fill="none" stroke={C.purple} strokeWidth={20} strokeLinecap="round" strokeDasharray={2 * Math.PI * r} strokeDashoffset={2 * Math.PI * r * (1 - seats)} />
            </svg>
          </div>
        </Tile>
        <Tile at={1335} label="Renewal"><Big><span style={{ fontSize: 66, letterSpacing: -2.5, whiteSpace: "nowrap" }}>March 14</span></Big></Tile>
        <div style={{ gridColumn: "1 / -1", borderRadius: 26, background: "#f6f2ff", padding: "30px 34px", opacity: frame >= 1350 ? 1 : 0 }}>
          <svg width="100%" height="100%" viewBox="0 0 1010 210" preserveAspectRatio="none" style={{ overflow: "visible" }}>
            <path d={CHART} fill="none" stroke={C.purple} strokeWidth={8} strokeLinecap="round" pathLength={1} strokeDasharray={1} strokeDashoffset={line} vectorEffect="non-scaling-stroke" />
          </svg>
        </div>
      </div>
    </div>
  );
};

/** The card's place in the frame once the phone has pulled back to its resting size. */
const cardEnd = () => {
  const s = SCALE * REST;
  return { x: PHONE_C.x + (cardCss.x * SCALE + SCREEN.x - PHONE_C.x) * REST, y: restY + PHONE_C.y + (cardCss.y * SCALE + SCREEN.y - PHONE_C.y) * REST, w: cardCss.w * s, h: cardCss.h * s };
};

const Computer: React.FC = () => {
  const frame = useCurrentFrame();
  const shrink = interpolate(frame, [1380, 1410], [0, 1], { ...clamp, easing: inOut });
  const hit = usePop(1260, 13);
  const end = cardEnd();
  const from = { x: 180, y: 150, w: 1560, h: 880 };
  const x = from.x + (end.x - from.x) * shrink;
  const y = from.y + (end.y - from.y) * shrink;
  const w = from.w + (end.w - from.w) * shrink;
  const h = from.h + (end.h - from.h) * shrink;
  return (
    <AbsoluteFill style={{ fontFamily, pointerEvents: "none" }}>
      {frame < 1381 && <AbsoluteFill style={{ background: `radial-gradient(circle at 30% 0%, ${C.purple}, #2a0a7a 70%)` }}><Ribbons start={1262} muted /></AbsoluteFill>}
      <div style={{ position: "absolute", left: x, top: y, width: w, height: h, overflow: "hidden", borderRadius: 26 - 12 * shrink, scale: frame < 1290 ? String(hit) : undefined, background: "#fff" }}>
        <div style={{ width: 1560, height: 880, scale: String(w / 1560), transformOrigin: "top left", opacity: 1 - interpolate(shrink, [0.6, 1], [0, 1], clamp) }}><Dashboard /></div>
      </div>
      {frame >= 1263 && frame < 1290 && <Sticker text="Everyone gets their own computer." at={1263} style={{ right: undefined, left: 180, top: 26 }} />}
      {frame >= 1290 && frame < 1380 && <Sticker text="It builds apps and dashboards too." at={1290} until={1380} style={{ right: undefined, left: 180, top: 26 }} />}
    </AbsoluteFill>
  );
};

/* ── The stage: one shot from the wipe at 300 to the fly-off at 1470. ── */

export const Stage: React.FC = () => {
  const frame = useCurrentFrame();
  const wipe = interpolate(frame, [300, 320], [0, 2300], { ...clamp, easing: Easing.bezier(0.5, 0, 0.3, 1) });
  // From 810 the phone's scale carries the zoom, so the camera sits still at its own zoom 1.
  const cam = frame < 810 ? stageCamera(frame) : { z: 1, x: endCamera.tx, y: endCamera.ty };
  const r = rig(frame);
  const i = Math.max(0, Math.min(morph.length - 1, frame - 810));
  const w = morph[i];
  const mask = frame >= 810 ? { x: endCamera.tx + SCALE * w.x, y: endCamera.ty + SCALE * w.y, w: SCALE * w.w, h: SCALE * w.h, r: SCALE * w.r } : null;
  const body = interpolate(frame, [870, 900], [0, 1], { ...clamp, easing: easeOut });
  const dim = interpolate(frame, [930, 962], [0, 1], { ...clamp, easing: inOut }) * interpolate(frame, [990, 995], [1, 0], clamp);
  const clockIn = interpolate(frame, [948, 960], [0, 1], clamp) * interpolate(frame, [990, 994], [1, 0], clamp);
  const sameThread = interpolate(frame, [900, 926], [0, 1], { ...clamp, easing: inOut }) * interpolate(frame, [930, 948], [1, 0], clamp);
  const sameRise = interpolate(frame, [900, 930], [22, 0], { ...clamp, easing: easeOut });
  const transform = `translate(${r.buzz}px, ${r.y}px) perspective(2400px) rotateY(${r.turn}deg) scale(${r.scale})`;
  const phoneHidden = frame >= 1260 && frame < 1381;
  return (
    <AbsoluteFill style={{ clipPath: frame < 320 ? `circle(${wipe}px at 1325px 510px)` : undefined, background: frame < 810 ? C.lavender : sky(frame), fontFamily, overflow: "hidden" }}>
      {!phoneHidden && (
        <AbsoluteFill style={{ transform, transformOrigin: `${PHONE_C.x}px ${PHONE_C.y}px`, transformStyle: "preserve-3d" }}>
          {body > 0 && <div style={{ position: "absolute", left: SCREEN.x - 15, top: SCREEN.y - 15, width: SCREEN.w + 30, height: SCREEN.h + 30, borderRadius: SCREEN.r + 15, background: "#08060f", boxShadow: "0 50px 120px #000a, 0 0 0 2px #453a6b", opacity: body, scale: String(1.035 - 0.035 * body) }} />}
          {frame < 966 && (
            <div style={{ position: "absolute", inset: 0, clipPath: mask ? `inset(${mask.y}px ${1920 - mask.x - mask.w}px ${1080 - mask.y - mask.h}px ${mask.x}px round ${mask.r}px)` : undefined }}>
              <div style={{ position: "absolute", left: cam.x, top: cam.y, width: 1920 * cam.z, height: 1200 * cam.z }}>
                <Sequence from={300} durationInFrames={666} layout="none"><Video src={staticFile("clips/desktop.mp4")} muted style={{ width: "100%", height: "100%", display: "block" }} /></Sequence>
              </div>
            </div>
          )}
          {frame >= 960 && (
            <div style={{ position: "absolute", left: SCREEN.x, top: SCREEN.y, width: SCREEN.w, height: SCREEN.h, borderRadius: SCREEN.r, overflow: "hidden", background: "#fff" }}>
              <Sequence from={960} durationInFrames={510} layout="none"><Video src={staticFile("clips/phone.mp4")} muted style={{ width: "100%", height: "100%", display: "block" }} /></Sequence>
            </div>
          )}
          {frame >= 900 && dim > 0 && (
            <div style={{ position: "absolute", left: SCREEN.x, top: SCREEN.y, width: SCREEN.w, height: SCREEN.h, borderRadius: SCREEN.r, background: `rgba(5,4,14,${dim})`, display: "grid", placeItems: "center" }}>
              <div style={{ opacity: clockIn, color: "#fff", fontSize: 92, fontWeight: 700, letterSpacing: -3, fontVariantNumeric: "tabular-nums" }}>{clockTime(frame)}</div>
            </div>
          )}
        </AbsoluteFill>
      )}
      {stickers.map(([from, to, text]) => frame >= from && frame < to && <Sticker key={text} text={text} at={from} until={to} style={from === 543 ? { right: undefined, top: undefined, left: 64, bottom: 72 } : { top: undefined, right: 64, bottom: 72 }} />)}
      {sameThread > 0 && <div style={{ position: "absolute", left: 56, top: 470, translate: `0 ${sameRise}px`, opacity: sameThread, color: "#fff", fontSize: 108, fontWeight: 800, letterSpacing: -4 }}>Same thread.</div>}
      {frame >= 1125 && frame < 1170 && <Sticker text="You didn't ask." at={1125} until={1170} size={84} style={{ right: undefined, left: 44, top: 470 }} />}
      {frame >= 1260 && frame < 1411 && <Computer />}
    </AbsoluteFill>
  );
};

/** The card leaves the phone and flies off as the trust cards land. */
export const CardFlyOff: React.FC = () => {
  const frame = useCurrentFrame();
  const t = interpolate(frame, [1462, 1482], [0, 1], { ...clamp, easing: easeIn });
  const end = cardEnd();
  if (frame < 1462 || frame >= 1482) return null;
  return (
    <div style={{ position: "absolute", left: end.x, top: end.y, width: end.w, height: end.h, translate: `${t * 1500}px ${-t * 900}px`, rotate: `${t * 28}deg`, scale: String(1 + t * 0.7), borderRadius: 16, background: "#fff", border: "1px solid #d4c9e9", display: "flex", alignItems: "center", gap: 14, padding: "0 18px", fontFamily, fontSize: 28, lineHeight: 1.2, fontWeight: 600, color: C.ink, boxShadow: "0 20px 50px #0003", whiteSpace: "nowrap", overflow: "hidden" }}>
      <span style={{ width: 64, height: 64, borderRadius: 16, background: C.lavender, flexShrink: 0, display: "grid", placeItems: "center" }}>
        <svg width={38} height={38} viewBox="0 0 24 24"><path d="M3 21V13M9 21V8M15 21V11M21 21V3" stroke={C.purple} strokeWidth={3.4} strokeLinecap="round" /></svg>
      </span>
      <span>Acme renewal dashboard.<br />Shared with Sam.</span>
    </div>
  );
};

/* ── Trust: four titles on the beat. ── */

const trust = ["Uses your permissions", "Asks before acting", "Logs every action", "Apps stay private"];

const TrustCard: React.FC<{ title: string; at: number; i: number }> = ({ title, at, i }) => {
  const pop = usePop(at, 12);
  const accent = i === 0 || i === 3;
  return (
    <div style={{ scale: String(pop), borderRadius: 34, padding: "40px 46px", background: accent ? C.purple : "#f1ebff", color: accent ? "#fff" : C.ink, display: "flex", alignItems: "center" }}>
      <div style={{ fontSize: 72, fontWeight: 850, letterSpacing: -2.2, lineHeight: 1.04 }}>{title}</div>
    </div>
  );
};

export const Trust: React.FC = () => (
  <AbsoluteFill style={{ background: "#fff", fontFamily }}>
    <div style={{ position: "absolute", inset: 64, display: "grid", gridTemplateColumns: "1fr 1fr", gridTemplateRows: "1fr 1fr", gap: 26 }}>
      {trust.map((title, i) => <TrustCard key={title} title={title} at={i * 15} i={i} />)}
    </div>
  </AbsoluteFill>
);

/* ── End: the deck's closing line, ribbons, and a wave from the mark. ── */

export const End: React.FC = () => {
  const frame = useCurrentFrame();
  const one = usePop(0, 14);
  const two = usePop(30, 12);
  const mark = usePop(90, 10);
  const ask = usePop(105, 14);
  const blink = interpolate(frame, [150, 153, 156], [0, 1, 0], clamp);
  return (
    <AbsoluteFill style={{ background: "#fff", fontFamily }}>
      <Ribbons start={30} dur={40} />
      <div style={{ position: "absolute", left: 120, top: 230, fontSize: 124, fontWeight: 800, letterSpacing: -4.5, lineHeight: 1.1 }}>
        <div style={{ color: C.ink, scale: String(one), transformOrigin: "left center" }}>We delivered chat.</div>
        <div style={{ color: C.purple, scale: String(two), transformOrigin: "left center" }}>Now let's deliver<br />an assistant.</div>
      </div>
      <div style={{ position: "absolute", left: 120, top: 700, display: "flex", alignItems: "center", gap: 26 }}>
        <div style={{ scale: String(mark) }}><Onigiri size={92} happy={frame > 94 && frame < 140 ? 1 : 0} blink={blink} /></div>
        <div style={{ fontSize: 46, fontWeight: 700, color: C.ink, lineHeight: 1.15, scale: String(ask), transformOrigin: "left center" }}>Nori, inside Nova.<br /><span style={{ fontWeight: 500, opacity: 0.7 }}>Six-week pilot. One sales team.</span></div>
      </div>
    </AbsoluteFill>
  );
};
