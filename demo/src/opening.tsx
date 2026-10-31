import React from "react";
import { AbsoluteFill, Freeze, interpolate, useCurrentFrame } from "remotion";
import { C, Sticker, Onigiri, Ribbons, RICE_PATH, clamp, easeOut, fontFamily, usePop } from "./kit";

/* ── Scene 1: the chores. An unbranded chat app, faster every 30 frames. ── */

const grey = { line: "#e3e1e8", text: "#2b2933", soft: "#8b8894", side: "#f3f2f5", hover: "#e7e5ec" };
const oldChats = ["Acme email", "Acme email (2)", "Q2 numbers", "Renewal new try", "Untitled chat"];
const files = ["Acme_notes.pdf", "Q2_numbers.xlsx", "Renewal_call.docx"];
const models = ["Fast", "Balanced", "Advanced"];
const explain = "Acme is a renewal customer. Their contract ends in March. Last time we talked about pricing, and I like short emails, and...";

const Pointer: React.FC<{ x: number; y: number; down: boolean }> = ({ x, y, down }) => (
  <svg width={46} height={46} viewBox="0 0 24 24" style={{ position: "absolute", left: x - 8, top: y - 5, scale: down ? "0.82" : "1", transformOrigin: "8px 5px", filter: "drop-shadow(0 3px 5px #0005)" }}>
    <path d="M5 3l14 8-6.5 1.6L9.5 19z" fill="#120a2b" stroke="#fff" strokeWidth={1.6} strokeLinejoin="round" />
  </svg>
);

/** The cursor's path: (frame, x, y) stops, moving faster as the scene goes on. */
const cursorStops: [number, number, number][] = [[0, 170, 92], [8, 170, 92], [26, 760, 62], [30, 620, 62], [44, 620, 62], [56, 1140, 500], [60, 1140, 540], [82, 1140, 540], [90, 880, 660], [110, 1200, 720], [118, 180, 500], [124, 1500, 300], [130, 500, 800], [136, 1700, 700], [142, 900, 150], [149, 1300, 900]];

const File: React.FC<{ name: string; i: number }> = ({ name, i }) => {
  const pop = usePop(60 + i * 5, 10);
  const frame = useCurrentFrame();
  const fall = interpolate(frame, [60 + i * 5, 68 + i * 5], [-300, 0], { ...clamp, easing: easeOut });
  return (
    <div style={{ scale: String(pop), translate: `0 ${fall}px`, rotate: `${[-2, 1.5, -1][i]}deg`, display: "flex", alignItems: "center", gap: 14, padding: "14px 22px", borderRadius: 16, background: "#fff", border: `2px solid ${grey.line}`, boxShadow: "0 8px 24px #0000001a", fontSize: 26, fontWeight: 600, color: grey.text, whiteSpace: "nowrap" }}>
      <span style={{ width: 28, height: 34, borderRadius: 5, background: ["#ff4b62", "#2fa66a", "#3f7cf0"][i] }} />
      {name}
    </div>
  );
};

const ChatApp: React.FC<{ lively?: boolean; title?: string }> = ({ lively, title }) => {
  const frame = useCurrentFrame();
  const fresh = !lively || frame >= 2;
  const menu = lively && frame >= 30 && frame < 58;
  const typed = lively ? Math.round(interpolate(frame, [90, 116], [0, explain.length], clamp)) : 0;
  const pick = Math.floor(interpolate(frame, [32, 52], [0, 3], clamp)) % 3;
  const extra = lively ? Math.floor(interpolate(frame, [120, 149], [0, 14], clamp)) : 0;
  const sidebar = [...oldChats, ...Array.from({ length: extra }, (_, i) => oldChats[i % 5])];
  return (
    <div style={{ position: "absolute", inset: 0, display: "flex", background: "#fff", color: grey.text, fontFamily }}>
      <div style={{ width: 360, background: grey.side, borderRight: `2px solid ${grey.line}`, padding: "48px 22px", overflow: "hidden", flexShrink: 0 }}>
        <div style={{ padding: "16px 22px", borderRadius: 14, background: lively && frame < 5 ? grey.line : "#fff", border: `2px solid ${grey.line}`, fontSize: 30, fontWeight: 700 }}>+ New chat</div>
        <div style={{ marginTop: 26, display: "flex", flexDirection: "column", gap: 4 }}>
          {sidebar.map((chat, i) => <div key={i} style={{ padding: "13px 18px", borderRadius: 10, fontSize: 27, fontWeight: 500, whiteSpace: "nowrap", color: i >= 5 ? grey.text : "#55525f", background: i === 0 && !fresh ? grey.hover : undefined }}>{chat}</div>)}
        </div>
      </div>
      <div style={{ position: "relative", flex: 1 }}>
        <div style={{ position: "absolute", left: 44, top: 40, padding: "12px 22px", borderRadius: 12, background: menu ? grey.hover : "transparent", fontSize: 30, fontWeight: 700 }}>{title ?? "Balanced"} ▾</div>
        {menu && <div style={{ position: "absolute", left: 44, top: 108, width: 340, padding: 12, borderRadius: 20, background: "#fff", boxShadow: "0 18px 50px #0003", border: `2px solid ${grey.line}`, scale: String(Math.min(1, (frame - 30) / 4 + 0.6)), transformOrigin: "top left" }}>
          {models.map((model, i) => <div key={model} style={{ padding: "16px 22px", borderRadius: 12, fontSize: 32, fontWeight: 600, background: pick === i ? grey.hover : undefined }}>{model}</div>)}
        </div>}
        {!fresh && <div style={{ position: "absolute", left: 180, right: 180, top: 190, display: "flex", flexDirection: "column", gap: 28 }}>
          <div style={{ alignSelf: "flex-end", padding: "18px 26px", borderRadius: 22, background: grey.side, fontSize: 30 }}>Write the Acme email</div>
          <div style={{ fontSize: 30, lineHeight: 1.4 }}>Sure! Which Acme? What tone? What did you last discuss?</div>
        </div>}
        <div style={{ position: "absolute", left: 0, right: 0, bottom: 240, display: "flex", flexDirection: "column", alignItems: "center", gap: 24 }}>
          {fresh && <div style={{ fontSize: 66, fontWeight: 700, letterSpacing: -2, marginBottom: 26, opacity: typed > 40 ? 0.25 : 1 }}>How can I help?</div>}
          {lively && frame >= 60 && <div style={{ display: "flex", gap: 16 }}>{files.map((name, i) => <File key={name} name={name} i={i} />)}</div>}
          <div style={{ width: 1000, minHeight: 96, padding: "26px 34px", borderRadius: 30, border: `2px solid ${grey.line}`, boxShadow: "0 8px 30px #00000012", fontSize: 32, lineHeight: 1.34, background: "#fff", color: typed ? grey.text : grey.soft, fontWeight: 500 }}>
            {typed ? explain.slice(0, typed) : "Message"}
          </div>
        </div>
      </div>
    </div>
  );
};

const piles = [
  { at: 120, x: 400, y: 90, r: -6, t: "Fast" }, { at: 124, x: 980, y: 40, r: 5, t: "Advanced" }, { at: 128, x: 470, y: 470, r: 3, t: "Balanced" },
  { at: 132, x: 1080, y: 500, r: -5, t: "Fast" }, { at: 136, x: 720, y: 280, r: 7, t: "Advanced" }, { at: 140, x: 300, y: 300, r: -8, t: "Balanced" }, { at: 144, x: 1180, y: 220, r: 9, t: "Fast" },
];

const Pile: React.FC<(typeof piles)[number]> = ({ at, x, y, r, t }) => {
  const pop = usePop(at, 12);
  return (
    <div style={{ position: "absolute", left: x, top: y, width: 880, height: 495, rotate: `${r}deg`, scale: String(pop), borderRadius: 24, overflow: "hidden", boxShadow: "0 30px 80px #0007" }}>
      <div style={{ width: 1920, height: 1080, scale: "0.4583", transformOrigin: "top left" }}><ChatApp title={t} /></div>
    </div>
  );
};

const captions: [number, number, string][] = [[2, 30, "New chat."], [30, 60, "Pick a model."], [60, 90, "Add the files."], [90, 120, "Explain it again."], [120, 150, "Tomorrow, do it all again."]];

/** Everything on screen by frame 149, minus the caption; scene 2 rolls this frozen mess up. */
const Mess: React.FC = () => {
  const frame = useCurrentFrame();
  const stop = cursorStops.findIndex(([f]) => f > frame);
  const b = cursorStops[stop === -1 ? cursorStops.length - 1 : stop];
  const a = cursorStops[Math.max(0, (stop === -1 ? cursorStops.length : stop) - 1)];
  const p = a === b ? 1 : easeOut((frame - a[0]) / (b[0] - a[0]));
  const down = [[0, 4], [30, 33], [60, 63], [120, 123]].some(([s, e]) => frame >= s && frame < e);
  const shake = interpolate(frame, [60, 149], [0, 1], clamp);
  const zoom = interpolate(frame, [0, 149], [1, 1.07], clamp);
  return (
    <AbsoluteFill style={{ background: C.night, fontFamily, overflow: "hidden" }}>
      <AbsoluteFill style={{ scale: String(zoom), translate: `${Math.sin(frame * 1.7) * 9 * shake}px ${Math.cos(frame * 2.3) * 6 * shake}px`, rotate: `${Math.sin(frame * 1.1) * 0.55 * shake}deg` }}>
        <ChatApp lively />
        {piles.map((w) => frame >= w.at && <Pile key={w.at} {...w} />)}
      </AbsoluteFill>
      <Pointer x={a[1] + (b[1] - a[1]) * p} y={a[2] + (b[2] - a[2]) * p} down={down} />
    </AbsoluteFill>
  );
};

/* ── Scene 2: a bamboo mat rolls the mess into rice, patted into the mark. ── */

const MARK = { x: 1325, y: 510 };
const MAT = { x: 60, y: 70, w: 1800, h: 940 };
const SLIDE: [number, number] = [150, 161];
const ROLL: [number, number] = [161, 181];
const TURN: [number, number] = [181, 189];
const BAMBOO = ["#c9c77c", "#b5b568", "#d6d48c", "#a8ab5e"];
const STRING = "#6b4a2b";
const STRINGS = [0.12, 0.31, 0.5, 0.69, 0.88];

/** A flat bamboo mat: slats side by side, bound by five lines of string. */
const Mat: React.FC = () => (
  <div style={{ position: "absolute", left: MAT.x, top: MAT.y, width: MAT.w, height: MAT.h, borderRadius: 10, overflow: "hidden", boxShadow: "0 12px 40px #0008" }}>
    <div style={{ display: "flex", height: "100%" }}>
      {Array.from({ length: 45 }, (_, i) => <div key={i} style={{ flex: 1, background: `linear-gradient(90deg, #0000, #fff3 30%, #0000 70%, #0003), ${BAMBOO[(i * 7) % 4]}`, borderRight: "2px solid #6f7238" }} />)}
    </div>
    {STRINGS.map((y) => <div key={y} style={{ position: "absolute", left: 0, right: 0, top: `${y * 100}%`, height: 6, background: `repeating-linear-gradient(90deg, ${STRING} 0 18px, #8a6339 18px 40px)` }} />)}
  </div>
);

/** The sheet the mat carries: the mat, with the frozen mess lying on it. */
const Sheet: React.FC<{ settle: number; slide: number }> = ({ settle, slide }) => (
  <AbsoluteFill>
    <div style={{ position: "absolute", inset: 0, translate: `${slide}px 0` }}><Mat /></div>
    <AbsoluteFill style={{ scale: String(1 - 0.17 * settle), rotate: `${-0.8 * settle}deg`, borderRadius: 20 * settle, overflow: "hidden", boxShadow: `0 14px 40px rgba(0,0,0,${0.45 * settle})` }}>
      <Freeze frame={149}><Mess /></Freeze>
    </AbsoluteFill>
  </AbsoluteFill>
);

/** What lies ahead of the roll: flat, then lifting and bunching up as it's pulled in. */
const PullIn: React.FC<{ contact: number; settle: number }> = ({ contact, settle }) => {
  const reach = 340;
  const slices = 10;
  const edges = Array.from({ length: slices + 1 }, (_, i) => contact + (reach * i) / slices);
  const at = (u: number) => contact + 0.42 * reach + 0.58 * reach * ((u - contact) / reach) ** 1.7;
  return (
    <>
      <div style={{ position: "absolute", left: contact + reach, top: 0, right: 0, bottom: 0, overflow: "hidden" }}>
        <div style={{ position: "absolute", left: -(contact + reach), top: 0, width: 1920, height: 1080 }}><Sheet settle={settle} slide={0} /></div>
      </div>
      {edges.slice(0, -1).map((u, i) => {
        const x0 = at(u), x1 = at(edges[i + 1]);
        return (
          <div key={i} style={{ position: "absolute", left: x0, top: 0, width: x1 - x0 + 1, height: 1080, overflow: "hidden" }}>
            <div style={{ position: "absolute", left: 0, top: 0, width: 1920, height: 1080, scale: `${(x1 - x0 + 1) / (edges[i + 1] - u)} 1`, transformOrigin: "left center" }}>
              <div style={{ position: "absolute", left: -u, top: 0, width: 1920, height: 1080 }}><Sheet settle={settle} slide={0} /></div>
            </div>
            <div style={{ position: "absolute", inset: 0, background: `rgba(30,20,0,${0.42 * (1 - i / slices)})` }} />
          </div>
        );
      })}
    </>
  );
};

const spiral = (r: number, turns: number, start: number) => {
  let d = "";
  for (let i = 0; i <= 90; i++) {
    const t = i / 90;
    const a = start + t * turns * 2 * Math.PI;
    const k = r * (0.1 + 0.9 * t);
    d += `${i ? "L" : "M"}${(Math.cos(a) * k).toFixed(1)} ${(Math.sin(a) * k).toFixed(1)}`;
  }
  return d;
};

/** The rolled mat: `open` turns the log from side-on (0) to its spiral end facing us (1). */
const Log: React.FC<{ cx: number; cy: number; r: number; length: number; open: number; spin: number; rice: number; rim?: number }> = ({ cx, cy, r, length, open, spin, rice, rim = 1 }) => {
  const ry = r * (0.3 + 0.7 * open);
  const slats = 13;
  return (
    <div style={{ position: "absolute", left: cx - r, top: cy - length / 2, width: 2 * r, height: length, opacity: rim, filter: "drop-shadow(0 22px 34px #000a)" }}>
      {length > 2 && (
        <div style={{ position: "absolute", left: 0, top: 0, width: 2 * r, height: length, overflow: "hidden", borderRadius: `0 0 ${r}px ${r}px / 0 0 ${ry}px ${ry}px` }}>
          {Array.from({ length: slats }, (_, i) => {
            const a0 = ((i + (spin % 1)) / slats) * Math.PI;
            const a1 = ((i + 1 + (spin % 1)) / slats) * Math.PI;
            const x0 = r * (1 - Math.cos(Math.min(a0, Math.PI)));
            const x1 = r * (1 - Math.cos(Math.min(a1, Math.PI)));
            return <div key={i} style={{ position: "absolute", left: x0, top: 0, width: Math.max(0, x1 - x0), height: "100%", background: BAMBOO[(i + Math.floor(spin) * 3) & 3], borderRight: "2px solid #6f7238" }} />;
          })}
          {STRINGS.map((y) => <div key={y} style={{ position: "absolute", left: 0, right: 0, top: `${y * 100}%`, height: 6, background: STRING }} />)}
          <div style={{ position: "absolute", inset: 0, background: "linear-gradient(90deg, #0008, #0000 30%, #fff4 48%, #0000 66%, #0009)" }} />
        </div>
      )}
      <svg width={2 * r} height={2 * ry} viewBox={`${-r} ${-ry} ${2 * r} ${2 * ry}`} style={{ position: "absolute", left: 0, top: -ry, overflow: "visible" }}>
        <g transform={`scale(1 ${ry / r})`}>
          <circle r={r} fill="#d6d48c" stroke="#6f7238" strokeWidth={3} />
          <circle r={r * Math.max(0.42, rice)} fill={C.paper} />
          <path d={spiral(r * 0.96, 2.6, spin * 2.4)} fill="none" stroke="#7a7a3f" strokeWidth={Math.max(5, r * 0.055)} strokeLinecap="round" />
        </g>
      </svg>
    </div>
  );
};

const body: [number, number][] = [
  [48, 11], [54.5, 11], [59.2, 14.3], [62.6, 20],
  [69.9, 32.8], [77.3, 45.6], [84.6, 58.4],
  [91.5, 70.5], [84.6, 85], [70.6, 85],
  [55.5, 85], [40.5, 85], [25.4, 85],
  [11.4, 85], [4.5, 70.5], [11.4, 58.4],
  [18.7, 45.6], [26.1, 32.8], [33.4, 20],
  [36.8, 14.3], [41.5, 11], [48, 11],
];

/** The mark's outline eased out of a ball: t=1 is exactly `RICE_PATH`. */
const ricePath = (t: number) => {
  const c = { x: 48, y: 52 };
  const pts = body.map(([x, y]) => {
    const a = Math.atan2(y - c.y, x - c.x);
    return [c.x + (Math.cos(a) * 37 - (x - c.x)) * (1 - t) + (x - c.x), c.y + (Math.sin(a) * 37 - (y - c.y)) * (1 - t) + (y - c.y)];
  });
  if (t >= 1) return RICE_PATH;
  let d = `M${pts[0][0]} ${pts[0][1]}`;
  for (let i = 1; i < pts.length; i += 3) d += `C${pts[i].join(" ")} ${pts[i + 1].join(" ")} ${pts[i + 2].join(" ")}`;
  return `${d}Z`;
};

const Rice: React.FC<{ t: number; size: number; nori: number }> = ({ t, size, nori }) => (
  <svg width={size} height={size} viewBox="0 0 96 96" style={{ overflow: "visible", display: "block" }}>
    <defs><clipPath id="rice"><path d={ricePath(t)} /></clipPath></defs>
    <path d={ricePath(t)} fill={C.paper} />
    {nori < 1 && nori > -60 && <rect x="30" y={61 + nori} width="36" height="30" rx="3" fill="#101a30" />}
    <path d={ricePath(t)} fill="none" stroke={C.ink} strokeWidth={1.75} strokeLinejoin="round" />
  </svg>
);

export const Scene1And2: React.FC = () => {
  const frame = useCurrentFrame();
  const slideIn = interpolate(frame, SLIDE, [-1920, 0], { ...clamp, easing: easeOut });
  const settle = interpolate(frame, SLIDE, [0, 1], { ...clamp, easing: easeOut });
  const rolled = interpolate(frame, ROLL, [0, 1], { ...clamp, easing: (t) => t * t * (3 - 2 * t) });
  const contact = -83 + rolled * 1800; // the sheet meets the log just past this point, ending at the mat's far edge
  const r = 34 + 118 * Math.sqrt(rolled);
  const turn = interpolate(frame, TURN, [0, 1], { ...clamp, easing: (t) => t * t * (3 - 2 * t) });
  // Side-on and rolling, then swinging to centre with its spiral end facing us, full of rice.
  const logX = (contact + 143 - r + 8) * (1 - turn) + 960 * turn;
  const logY = (MAT.y + MAT.h / 2) * (1 - turn) + 540 * turn;
  const logR = r * (1 - turn) + 205 * turn;
  const logLen = MAT.h * (1 - turn);
  const matOff = interpolate(frame, [189, 195], [1, 0], clamp);
  const ricePop = usePop(TURN[1] + 2, 9);
  const riceFill = interpolate(frame, [181, 190], [0.42, 1], clamp);
  const capOut = interpolate(frame, [150, 156], [1, 0], { ...clamp, easing: easeOut });

  // Each pat on the beat sharpens the corners and squashes the rice.
  const pats = [195, 210, 225];
  const t = interpolate(frame, [190, 197, 210, 212, 225, 227, 240], [0, 0.3, 0.3, 0.62, 0.62, 0.88, 1], clamp);
  const lastPat = [...pats].reverse().find((p) => frame >= p) ?? -99;
  const pat = interpolate(frame - lastPat, [0, 3, 7, 12], [1, 0.84, 1.05, 1], clamp);
  const drift = interpolate(frame, [228, 240], [0, 1], { ...clamp, easing: easeOut });
  const x = 960 + (MARK.x - 960) * drift;
  const y = 540 + (MARK.y - 540) * drift;
  const nori = interpolate(frame, [233, 240], [-240, 0], { ...clamp, easing: (p) => p * p });

  const land = interpolate(frame, [240, 244, 251, 258], [0.72, 0.72, 1.08, 1], clamp);
  const ring = interpolate(frame, [240, 249], [0.4, 1], { ...clamp, easing: easeOut });
  const eyes = interpolate(frame, [240, 247], [1, 0], clamp);
  const blink = interpolate(frame, [288, 291, 294], [0, 1, 0], clamp);
  const title = usePop(246, 12);
  const sub = usePop(258, 14);

  return (
    <AbsoluteFill style={{ background: C.night, fontFamily, overflow: "hidden" }}>
      {frame < SLIDE[1] + 1 && frame >= 150 && <Sheet settle={settle} slide={slideIn} />}
      {frame < 150 && <Mess />}
      {frame > SLIDE[1] && frame < ROLL[1] && <PullIn contact={contact} settle={1} />}
      {frame < 150 && captions.map(([from, to, text]) => frame >= from && frame < to && <Sticker key={text} text={text} at={from} />)}
      {frame >= 150 && capOut > 0 && <div style={{ position: "absolute", inset: 0, scale: String(capOut), transformOrigin: "1680px 110px" }}><Sticker text="Tomorrow, do it all again." at={120 - 150} /></div>}
      {frame >= TURN[1] + 2 && frame < 240 && (
        <div style={{ position: "absolute", left: x - 205, top: y - 205, scale: frame < 195 ? String(0.92 + 0.08 * ricePop) : `${2 - pat} ${pat}`, transformOrigin: "50% 88%" }}><Rice t={t} size={410} nori={nori} /></div>
      )}
      {frame > SLIDE[1] && frame < 195 && <Log cx={logX} cy={logY} r={logR} length={logLen} open={turn} spin={frame * 0.42} rice={riceFill} rim={matOff} />}
      {frame >= 240 && (
        <AbsoluteFill style={{ background: `radial-gradient(circle at 30% 0%, ${C.purple}, #2a0a7a 70%)` }}>
          <Ribbons start={240} muted />
          <div style={{ position: "absolute", left: MARK.x - 295, top: MARK.y - 295, width: 590, height: 590, borderRadius: "50%", background: "#f6f2f6", boxShadow: "0 0 0 34px #ffffff14, 0 0 0 70px #ffffff0a", display: "grid", placeItems: "center", scale: String(ring) }}>
            <div style={{ scale: `${2 - land} ${land}`, transformOrigin: "50% 88%" }}>
              <Onigiri size={410} happy={frame > 256 && frame < 282 ? 1 : 0} blink={Math.max(eyes, blink)} />
            </div>
          </div>
          <div style={{ position: "absolute", left: 130, top: 300, color: "#fff" }}>
            <div style={{ fontSize: 210, fontWeight: 900, letterSpacing: -8, lineHeight: 1, scale: String(title), transformOrigin: "left center" }}>Nori</div>
            <div style={{ marginTop: 18, fontSize: 64, fontWeight: 600, color: "#d9ccff", letterSpacing: -1, scale: String(sub), transformOrigin: "left center" }}>The assistant inside Nova</div>
          </div>
        </AbsoluteFill>
      )}
    </AbsoluteFill>
  );
};
