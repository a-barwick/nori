import { Video } from "@remotion/media";
import React from "react";
import { AbsoluteFill, Sequence, interpolate, staticFile, useCurrentFrame } from "remotion";
import { C, Onigiri, Ribbons, Small, clamp, easeOut, fontFamily, usePop } from "./kit";
import timeline from "./timeline.json";

const Page: React.FC<{ bg: string; children: React.ReactNode }> = ({ bg, children }) => (
  <AbsoluteFill style={{ background: bg, fontFamily, overflow: "hidden" }}>{children}</AbsoluteFill>
);

/* ── Cold open: the chat windows pile up, one per beat. ───────────────── */

const windows = [
  { x: 150, y: 110, r: -5, at: 0 }, { x: 1010, y: 90, r: 4, at: 15 }, { x: 560, y: 450, r: -2, at: 30 }, { x: 1130, y: 520, r: 6, at: 45 },
  { x: 90, y: 560, r: 3, at: 52 }, { x: 760, y: 130, r: -7, at: 60 }, { x: 330, y: 300, r: 5, at: 67 }, { x: 880, y: 330, r: -4, at: 75 },
];

const ChatWindow: React.FC<{ x: number; y: number; r: number; at: number }> = ({ x, y, r, at }) => {
  const pop = usePop(at, 13);
  if (pop <= 0) return null;
  return (
    <div style={{ position: "absolute", left: x, top: y, width: 680, height: 430, rotate: `${r}deg`, scale: String(pop), borderRadius: 26, background: "#fff", boxShadow: "0 30px 80px #0009", padding: 26, display: "flex", flexDirection: "column" }}>
      <div style={{ alignSelf: "flex-start", padding: "12px 24px", borderRadius: 14, background: C.purple, color: "#fff", fontSize: 30, fontWeight: 700 }}>+ New chat</div>
      <div style={{ flex: 1, display: "grid", placeItems: "center", fontSize: 34, color: "#8a84a3", fontWeight: 500 }}>How can I help you today?</div>
      <div style={{ height: 66, borderRadius: 33, border: "2px solid #e4def2" }} />
    </div>
  );
};

export const ColdOpen: React.FC = () => {
  const frame = useCurrentFrame();
  const line = usePop(90, 12);
  return (
    <Page bg={C.night}>
      <AbsoluteFill style={{ filter: `brightness(${interpolate(frame, [86, 94], [1, 0.28], clamp)})` }}>
        {windows.map((w) => <ChatWindow key={w.at} {...w} />)}
      </AbsoluteFill>
      {frame >= 90 && (
        <AbsoluteFill style={{ display: "grid", placeItems: "center" }}>
          <div style={{ scale: String(line), fontSize: 148, fontWeight: 900, letterSpacing: -5, color: "#fff", textAlign: "center", lineHeight: 1.02 }}>
            It forgot you.<br /><span style={{ color: C.coral }}>Again.</span>
          </div>
        </AbsoluteFill>
      )}
    </Page>
  );
};

/* ── Stat: the 70% from the deck, drawn as a work week. ───────────────── */

const days = ["Mon", "Tue", "Wed", "Thu", "Fri"];

export const Stat: React.FC = () => {
  const frame = useCurrentFrame();
  const count = Math.round(interpolate(frame, [4, 34], [0, 70], { ...clamp, easing: easeOut }));
  const head = usePop(0, 14);
  const fill = (i: number) => interpolate(frame, [30 + i * 6, 38 + i * 6], [0, 1], { ...clamp, easing: easeOut });
  return (
    <Page bg="#fff">
      <div style={{ position: "absolute", left: 120, top: 92, scale: String(head), transformOrigin: "left center" }}>
        <div style={{ display: "flex", alignItems: "baseline", gap: 34 }}>
          <span style={{ fontSize: 300, fontWeight: 900, letterSpacing: -14, color: C.purple, lineHeight: 0.95, fontVariantNumeric: "tabular-nums" }}>{count}%</span>
          <span style={{ fontSize: 76, fontWeight: 800, letterSpacing: -2.5, color: C.ink, lineHeight: 1.04 }}>of a rep's week<br />isn't selling.</span>
        </div>
      </div>
      <div style={{ position: "absolute", left: 120, right: 120, top: 480, display: "flex", gap: 14 }}>
        {days.map((day, i) => {
          const coral = i < 3 ? 1 : i === 3 ? 0.5 : 0;
          return (
            <div key={day} style={{ flex: 1 }}>
              <div style={{ position: "relative", height: 210, borderRadius: 22, background: "#f1ebff", overflow: "hidden" }}>
                <div style={{ position: "absolute", inset: 0, width: `${coral * 100 * fill(i)}%`, background: C.coral }} />
                {coral < 1 && <div style={{ position: "absolute", top: 0, bottom: 0, left: `${coral * 100}%`, width: `${(1 - coral) * 100 * fill(i + 1)}%`, background: C.purple }} />}
              </div>
              <div style={{ marginTop: 16, fontSize: 38, fontWeight: 700, color: C.ink }}>{day}</div>
            </div>
          );
        })}
      </div>
      <div style={{ position: "absolute", left: 120, top: 806, display: "flex", gap: 70, opacity: interpolate(frame, [58, 70], [0, 1], clamp), fontSize: 46, fontWeight: 700, color: C.ink }}>
        <span><span style={{ color: C.coral }}>3½ days</span> admin and prep</span>
        <span><span style={{ color: C.purple }}>1½ days</span> selling</span>
      </div>
      <Small style={{ position: "absolute", left: 120, bottom: 62 }}>Salesforce, State of Sales, 6th edition (2024). 5,500 sales professionals.</Small>
    </Page>
  );
};

/* ── Meet Nori: the mark lands and squashes on the drop. ──────────────── */

export const Meet: React.FC = () => {
  const frame = useCurrentFrame();
  const drop = interpolate(frame, [0, 9], [-720, 0], { ...clamp, easing: (t) => t * t });
  const squash = interpolate(frame, [9, 13, 20, 27], [1, 0.72, 1.08, 1], clamp);
  const blink = interpolate(frame, [58, 61, 64], [0, 1, 0], clamp);
  const title = usePop(15, 12);
  const sub = usePop(27, 14);
  return (
    <Page bg={`radial-gradient(circle at 30% 0%, ${C.purple}, #2a0a7a 70%)`}>
      <Ribbons start={6} muted />
      <div style={{ position: "absolute", right: 200, top: 215, width: 590, height: 590, borderRadius: "50%", background: "#f6f2f6", boxShadow: "0 0 0 34px #ffffff14, 0 0 0 70px #ffffff0a", display: "grid", placeItems: "center", scale: String(interpolate(frame, [0, 9], [0.4, 1], { ...clamp, easing: easeOut })) }}>
        <div style={{ translate: `0 ${drop}px`, scale: `${2 - squash} ${squash}`, transformOrigin: "50% 88%" }}>
          <Onigiri size={410} happy={frame > 13 && frame < 54 ? 1 : 0} blink={blink} />
        </div>
      </div>
      <div style={{ position: "absolute", left: 130, top: 300, color: "#fff" }}>
        <div style={{ fontSize: 210, fontWeight: 900, letterSpacing: -8, lineHeight: 1, scale: String(title), transformOrigin: "left center" }}>Nori</div>
        <div style={{ marginTop: 18, fontSize: 64, fontWeight: 600, color: "#d9ccff", letterSpacing: -1, scale: String(sub), transformOrigin: "left center" }}>The assistant inside Nova</div>
      </div>
    </Page>
  );
};

/* ── Demo: the real interface, with a camera and sticker captions. ───── */

type Segment = (typeof timeline.demo)[number];
const WIDTH = 1920;
const HEIGHT = 1080;
const VIDEO_H = 1200;

const Footage: React.FC<{ segment: Segment }> = ({ segment }) => {
  const frame = useCurrentFrame();
  const keys = segment.camera.map((k) => k[0] * segment.frames);
  const at = (i: number) => interpolate(frame, keys, segment.camera.map((k) => k[i]), { ...clamp, easing: easeOut });
  const z = at(1);
  const w = WIDTH * z;
  const h = VIDEO_H * z;
  // Hold the point (cx, cy) of the clip at frame centre, without showing past its edges.
  const x = Math.min(0, Math.max(WIDTH - w, WIDTH / 2 - at(2) * w));
  const y = Math.min(0, Math.max(HEIGHT - h, HEIGHT / 2 - at(3) * h));
  return (
    <div style={{ position: "absolute", left: x, top: y, width: w, height: h }}>
      <Video src={staticFile("clips/desktop.mp4")} muted trimBefore={Math.round(segment.src[0] * 30)} playbackRate={((segment.src[1] - segment.src[0]) * 30) / segment.frames} style={{ width: "100%", height: "100%" }} />
    </div>
  );
};

const stickerPlace: Record<string, React.CSSProperties> = {
  top: { left: 0, right: 0, top: 54, justifyContent: "center" },
  topLeft: { left: 64, top: 60 },
  topRight: { right: 64, top: 60 },
  midRight: { right: 64, top: 190 },
  bottomRight: { right: 64, bottom: 84 },
};

const Sticker: React.FC<{ text: string; at: string; frames: number }> = ({ text, at, frames }) => {
  const frame = useCurrentFrame();
  const pop = usePop(3, 12);
  const out = interpolate(frame, [frames - 5, frames], [1, 0], clamp);
  return (
    <div style={{ position: "absolute", display: "flex", ...stickerPlace[at] }}>
      <div style={{ scale: String(pop * out), rotate: "-1.5deg", padding: "20px 38px", borderRadius: 26, background: C.purple, color: "#fff", fontSize: 66, fontWeight: 800, letterSpacing: -1.8, boxShadow: "0 18px 50px #3a00b855", maxWidth: at === "midRight" ? 800 : 1180, lineHeight: 1.08 }}>{text}</div>
    </div>
  );
};

export const Demo: React.FC = () => {
  let from = 0;
  return (
    <Page bg={C.lavender}>
      {timeline.demo.map((segment) => {
        const start = from;
        from += segment.frames;
        return (
          <Sequence key={segment.id} from={start} durationInFrames={segment.frames} name={segment.id}>
            <Footage segment={segment} />
            <Sticker text={segment.caption} at={segment.at} frames={segment.frames} />
          </Sequence>
        );
      })}
      <div style={{ position: "absolute", right: 28, bottom: 22, padding: "7px 16px", borderRadius: 12, background: "#fff", fontSize: 21, fontWeight: 500, color: C.ink }}>
        Real Nori interface. Scripted demo. Figures from Docusign's public Q2 FY27 release.
      </div>
    </Page>
  );
};

/* ── Phone: the same thread, on the train. ───────────────────────────── */

export const Phone: React.FC = () => {
  const frame = useCurrentFrame();
  const rise = interpolate(frame, [0, 14], [900, 0], { ...clamp, easing: easeOut });
  const tilt = interpolate(frame, [0, 120], [5, -3], clamp);
  const title = usePop(8, 13);
  const sub = usePop(62, 13);
  return (
    <Page bg={C.night}>
      <Ribbons start={2} muted />
      <div style={{ position: "absolute", left: 130, top: 330, color: "#fff" }}>
        <div style={{ fontSize: 118, fontWeight: 900, letterSpacing: -4.5, lineHeight: 1.02, scale: String(title), transformOrigin: "left center" }}>Same thread.<br />On your phone.</div>
        <div style={{ marginTop: 34, fontSize: 50, fontWeight: 600, color: C.lilac, scale: String(sub), transformOrigin: "left center" }}>Ask on the train. It keeps working.</div>
      </div>
      <div style={{ position: "absolute", right: 250, top: 76, width: 436, height: 928, translate: `0 ${rise}px`, rotate: `${tilt}deg`, borderRadius: 66, background: "#000", padding: 12, boxShadow: "0 50px 120px #000a, 0 0 0 2px #453a6b" }}>
        <div style={{ width: "100%", height: "100%", borderRadius: 54, overflow: "hidden", background: "#fff" }}>
          <Video src={staticFile("clips/phone.mp4")} muted trimBefore={9} playbackRate={2.2} style={{ width: "100%", height: "100%", objectFit: "cover" }} />
        </div>
      </div>
    </Page>
  );
};

/* ── Trust: the deck's four controls land on the beat. ───────────────── */

const trust = [
  ["Uses your permissions", "Signs in with Docusign SSO and sees only what you can see."],
  ["Asks before acting", "Email, Salesforce changes and sharing need your OK."],
  ["Logs every action", "You can review what it did and when."],
  ["Apps stay private", "What it builds stays behind SSO, with a named owner."],
];

const TrustCard: React.FC<{ title: string; body: string; at: number; i: number }> = ({ title, body, at, i }) => {
  const pop = usePop(at, 12);
  const accent = i % 2 === (i < 2 ? 0 : 1);
  return (
    <div style={{ scale: String(pop), borderRadius: 34, padding: "40px 46px", background: accent ? C.purple : "#f1ebff", color: accent ? "#fff" : C.ink, display: "flex", flexDirection: "column", justifyContent: "center", gap: 14 }}>
      <div style={{ fontSize: 66, fontWeight: 850, letterSpacing: -2, lineHeight: 1.04 }}>{title}</div>
      <div style={{ fontSize: 35, fontWeight: 500, lineHeight: 1.22, opacity: 0.86 }}>{body}</div>
    </div>
  );
};

export const Trust: React.FC = () => (
  <Page bg="#fff">
    <div style={{ position: "absolute", inset: 64, display: "grid", gridTemplateColumns: "1fr 1fr", gridTemplateRows: "1fr 1fr", gap: 26 }}>
      {trust.map(([title, body], i) => <TrustCard key={title} title={title} body={body} at={i * 15} i={i} />)}
    </div>
  </Page>
);

/* ── End: the deck's closing line, ribbons, and a wave from the mark. ── */

export const End: React.FC = () => {
  const frame = useCurrentFrame();
  const one = usePop(0, 14);
  const two = usePop(30, 12);
  const mark = usePop(62, 10);
  const ask = usePop(84, 14);
  const blink = interpolate(frame, [120, 123, 126], [0, 1, 0], clamp);
  return (
    <Page bg="#fff">
      <Ribbons start={26} dur={40} />
      <div style={{ position: "absolute", left: 120, top: 230, fontSize: 124, fontWeight: 800, letterSpacing: -4.5, lineHeight: 1.1 }}>
        <div style={{ color: C.ink, scale: String(one), transformOrigin: "left center" }}>We delivered chat.</div>
        <div style={{ color: C.purple, scale: String(two), transformOrigin: "left center" }}>Now let's deliver<br />an assistant.</div>
      </div>
      <div style={{ position: "absolute", left: 120, top: 700, display: "flex", alignItems: "center", gap: 26, scale: String(ask), transformOrigin: "left center" }}>
        <div style={{ scale: String(mark) }}><Onigiri size={92} happy={frame > 66 && frame < 110 ? 1 : 0} blink={blink} /></div>
        <div style={{ fontSize: 46, fontWeight: 700, color: C.ink, lineHeight: 1.15 }}>Nori, inside Nova.<br /><span style={{ fontWeight: 500, opacity: 0.7 }}>Six-week pilot. One sales team.</span></div>
      </div>
    </Page>
  );
};
