import React from "react";
import { AbsoluteFill, Freeze, interpolate, useCurrentFrame } from "remotion";
import { C, Sticker, Onigiri, Ribbons, RICE_PATH, clamp, easeOut, fontFamily, usePop } from "./kit";

/* ── Scene 1: the chores. An unbranded chat app, then a pile of them. ── */

const grey = { line: "#e3e1e8", text: "#2b2933", soft: "#8b8894", side: "#f3f2f5", hover: "#e7e5ec" };
const oldChats = ["Acme email", "Acme email (2)", "Q2 numbers", "Renewal new try", "Untitled chat"];
const files = ["Acme_notes.pdf", "Q2_numbers.xlsx", "Renewal_call.docx"];
const explain = "Acme is a renewal customer. Their contract ends in March. Last time we talked about pricing, and I like short emails, and...";

const Pointer: React.FC<{ x: number; y: number; down: boolean }> = ({ x, y, down }) => (
  <svg width={46} height={46} viewBox="0 0 24 24" style={{ position: "absolute", left: x - 8, top: y - 5, scale: down ? "0.82" : "1", transformOrigin: "8px 5px", filter: "drop-shadow(0 3px 5px #0005)" }}>
    <path d="M5 3l14 8-6.5 1.6L9.5 19z" fill="#120a2b" stroke="#fff" strokeWidth={1.6} strokeLinejoin="round" />
  </svg>
);

/** The cursor's path: (frame, x, y) stops, moving faster as the scene goes on. */
const cursorStops: [number, number, number][] = [[0, 170, 92], [10, 170, 92], [50, 1140, 540], [110, 1140, 540], [120, 880, 660], [170, 1200, 720], [205, 1200, 720], [212, 180, 500], [220, 1500, 300], [228, 500, 800], [236, 1700, 700], [246, 900, 150], [258, 1300, 900], [284, 1000, 400]];

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
  const typed = lively ? Math.round(interpolate(frame, [120, 170], [0, explain.length], clamp)) : 0;
  const extra = lively ? Math.floor(interpolate(frame, [210, 284], [0, 14], clamp)) : 0;
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
        <div style={{ position: "absolute", left: 44, top: 40, padding: "12px 22px", borderRadius: 12, fontSize: 30, fontWeight: 700 }}>{title ?? "New chat"}</div>
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
  { at: 210, x: 400, y: 90, r: -6, t: "Acme email" }, { at: 218, x: 980, y: 40, r: 5, t: "Q2 numbers" }, { at: 226, x: 470, y: 470, r: 3, t: "Untitled chat" },
  { at: 234, x: 1080, y: 500, r: -5, t: "Acme email (2)" }, { at: 242, x: 720, y: 280, r: 7, t: "Renewal new try" }, { at: 250, x: 300, y: 300, r: -8, t: "Untitled chat" }, { at: 258, x: 1180, y: 220, r: 9, t: "Acme email (3)" },
];

const Pile: React.FC<(typeof piles)[number]> = ({ at, x, y, r, t }) => {
  const pop = usePop(at, 12);
  return (
    <div style={{ position: "absolute", left: x, top: y, width: 880, height: 495, rotate: `${r}deg`, scale: String(pop), borderRadius: 24, overflow: "hidden", boxShadow: "0 30px 80px #0007" }}>
      <div style={{ width: 1920, height: 1080, scale: "0.4583", transformOrigin: "top left" }}><ChatApp title={t} /></div>
    </div>
  );
};

const captions: [number, number, string][] = [[2, 60, "New chat."], [60, 120, "Add the files."], [120, 210, "Explain it again."], [210, 285, "Tomorrow, do it all again."]];

/** Everything on screen by frame 284, minus the caption; scene 2 rolls this frozen mess up. */
const Mess: React.FC = () => {
  const frame = useCurrentFrame();
  const stop = cursorStops.findIndex(([f]) => f > frame);
  const b = cursorStops[stop === -1 ? cursorStops.length - 1 : stop];
  const a = cursorStops[Math.max(0, (stop === -1 ? cursorStops.length : stop) - 1)];
  const p = a === b ? 1 : easeOut((frame - a[0]) / (b[0] - a[0]));
  const down = [[0, 4], [60, 63], [210, 213]].some(([s, e]) => frame >= s && frame < e);
  const shake = interpolate(frame, [120, 284], [0, 1], clamp);
  const zoom = interpolate(frame, [0, 284], [1, 1.07], clamp);
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

/* ── Scene 2: three walls press the mess into rice, patted into the mark. ── */

const SHIFT = 135; // the press's frames below count from 150, and the film's start at 285
const MARK = { x: 1325, y: 510 };
const PURPLE_BG = `radial-gradient(circle at 30% 0%, ${C.purple}, #2a0a7a 70%)`;

/** One wall per side of the rice: outward normal, the line it stops on, the screen-clearing distance, its beat. */
const WALLS = [
  { nx: -0.868, ny: -0.497, stop: -988.1, from: 1010, beat: 165 },
  { nx: 0.868, ny: -0.497, stop: 678.5, from: 1010, beat: 180 },
  { nx: 0, ny: 1, stop: 698, from: 400, beat: 195 },
];

const wallOffset = (frame: number, { from, beat }: (typeof WALLS)[number]) =>
  frame < beat
    ? interpolate(frame, [beat - 7, beat], [from, 0], { ...clamp, easing: (t) => t * t * t })
    : interpolate(frame, [beat, beat + 3, beat + 8], [0, 12, 0], clamp);

const Walls: React.FC<{ frame: number }> = ({ frame }) => (
  <svg width={1920} height={1080} style={{ position: "absolute", inset: 0 }}>
    {WALLS.map((wall) => {
      const d = wall.stop + wallOffset(frame, wall);
      const angle = (Math.atan2(wall.ny, wall.nx) * 180) / Math.PI;
      return (
        <g key={wall.beat} transform={`translate(${wall.nx * d} ${wall.ny * d}) rotate(${angle})`}>
          <rect x={0} y={-3000} width={3000} height={6000} fill={C.purple} />
        </g>
      );
    })}
  </svg>
);

/** The frozen mess inside the walls: squeezed smaller and whiter on each beat. */
const Pressed: React.FC<{ frame: number }> = ({ frame }) => {
  const squeeze = interpolate(frame, [158, 165, 173, 180, 188, 195], [1, 0.74, 0.74, 0.52, 0.52, 0.34], clamp);
  const white = interpolate(frame, [160, 165, 175, 180, 190, 196], [0, 0.22, 0.22, 0.58, 0.58, 1], clamp);
  return (
    <AbsoluteFill style={{ background: C.lavender }}>
      <AbsoluteFill style={{ scale: String(squeeze), borderRadius: 40, overflow: "hidden" }}>
        <Freeze frame={284}><Mess /></Freeze>
      </AbsoluteFill>
      <AbsoluteFill style={{ background: C.paper, opacity: white }} />
    </AbsoluteFill>
  );
};

const Rice: React.FC<{ size: number; nori: number }> = ({ size, nori }) => (
  <svg width={size} height={size} viewBox="0 0 96 96" style={{ overflow: "visible", display: "block" }}>
    <defs><clipPath id="rice"><path d={RICE_PATH} /></clipPath></defs>
    <path d={RICE_PATH} fill={C.paper} />
    {nori < 1 && nori > -60 && <rect x="30" y={61 + nori} width="36" height="30" rx="3" fill="#101a30" />}
    <path d={RICE_PATH} fill="none" stroke={C.ink} strokeWidth={1.75} strokeLinejoin="round" />
  </svg>
);

/** The opening card: the frozen first frame of the chat app waits underneath as it lifts. */
export const Intro: React.FC = () => {
  const frame = useCurrentFrame();
  const title = usePop(8, 12);
  const lift = interpolate(frame, [60, 75], [0, -1080], { ...clamp, easing: (t) => t * t * (3 - 2 * t) });
  return (
    <AbsoluteFill style={{ background: PURPLE_BG, translate: `0 ${lift}px`, fontFamily, overflow: "hidden" }}>
      <Ribbons start={4} muted />
      <div style={{ position: "absolute", left: 130, top: 400, color: "#fff", fontSize: 190, fontWeight: 900, letterSpacing: -7, scale: String(title), transformOrigin: "left center" }}>The problem</div>
    </AbsoluteFill>
  );
};

export const Scene1And2: React.FC = () => {
  const real = useCurrentFrame();
  const frame = real - SHIFT;
  const capOut = interpolate(frame, [150, 156], [1, 0], { ...clamp, easing: easeOut });

  // The walls finish on 195. Rice takes over at 203 with a squash, and one more pat on 225.
  const pats = [203, 225];
  const lastPat = [...pats].reverse().find((p) => frame >= p) ?? -99;
  const pat = interpolate(frame - lastPat, [0, 3, 7, 12], [1, 0.84, 1.05, 1], clamp);
  const drift = interpolate(frame, [228, 240], [0, 1], { ...clamp, easing: easeOut });
  const x = 960 + (MARK.x - 960) * drift;
  const y = 540 + (MARK.y - 540) * drift;
  const nori = interpolate(frame, [233, 240], [-240, 0], { ...clamp, easing: (p) => p * p });

  const land = interpolate(frame, [240, 244, 251, 258], [0.72, 0.72, 1.08, 1], clamp);
  const ring = interpolate(frame, [240, 249], [0.4, 1], { ...clamp, easing: easeOut });
  const eyes = interpolate(frame, [240, 247], [1, 0], clamp);
  const blink = interpolate(frame, [325, 328, 331], [0, 1, 0], clamp);
  const title = usePop(246 + SHIFT, 12);
  const sub = usePop(258 + SHIFT, 14);

  return (
    <AbsoluteFill style={{ background: C.night, fontFamily, overflow: "hidden" }}>
      {real < 285 && <Mess />}
      {real < 285 && captions.map(([from, to, text]) => real >= from && real < to && <Sticker key={text} text={text} at={from} />)}
      {real >= 285 && frame < 204 && <Pressed frame={frame} />}
      {real >= 285 && capOut > 0 && <div style={{ position: "absolute", inset: 0, scale: String(capOut), transformOrigin: "1680px 110px" }}><Sticker text="Tomorrow, do it all again." at={210} /></div>}
      {real >= 285 && frame < 204 && <Walls frame={frame} />}
      {frame >= 203 && frame < 240 && <AbsoluteFill style={{ background: PURPLE_BG }} />}
      {frame >= 203 && frame < 240 && (
        <div style={{ position: "absolute", left: x - 205, top: y - 205, scale: `${2 - pat} ${pat}`, transformOrigin: "50% 88%" }}><Rice size={410} nori={nori} /></div>
      )}
      {frame >= 240 && (
        <AbsoluteFill style={{ background: PURPLE_BG }}>
          <Ribbons start={240 + SHIFT} muted />
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
