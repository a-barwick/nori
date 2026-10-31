import { loadFont } from "@remotion/google-fonts/Figtree";
import React from "react";
import { Easing, interpolate, spring, useCurrentFrame, useVideoConfig } from "remotion";

export const { fontFamily } = loadFont("normal", { weights: ["400", "500", "600", "700", "800", "900"], subsets: ["latin"] });

export const C = {
  purple: "#5000ff",
  coral: "#ff4b62",
  ink: "#190936",
  night: "#120a2b",
  lavender: "#eee5fc",
  lilac: "#a98bff",
  paper: "#fdfcff",
};

export const clamp = { extrapolateLeft: "clamp", extrapolateRight: "clamp" } as const;
export const easeOut = Easing.bezier(0.16, 1, 0.3, 1);

/** Pop from nothing with a little overshoot, starting at `at`. */
export const usePop = (at: number, damping = 11) => {
  const frame = useCurrentFrame();
  const { fps } = useVideoConfig();
  return spring({ frame: frame - at, fps, config: { damping, stiffness: 190, mass: 0.7 } });
};

/** The real mark from web/src/components/mark/NoriMark.tsx, with a frame-driven face. */
export const Onigiri: React.FC<{ size: number; happy?: number; blink?: number }> = ({ size, happy = 0, blink = 0 }) => {
  const body = "M48 11C54.5 11 59.2 14.3 62.6 20L84.6 58.4C91.5 70.5 84.6 85 70.6 85H25.4C11.4 85 4.5 70.5 11.4 58.4L33.4 20C36.8 14.3 41.5 11 48 11Z";
  const face = { stroke: C.ink, strokeWidth: 2.6, strokeLinecap: "round" as const, fill: "none" };
  const id = `oni-${size}`;
  return (
    <svg width={size} height={size} viewBox="0 0 96 96" style={{ overflow: "visible", display: "block" }}>
      <defs><clipPath id={id}><path d={body} /></clipPath></defs>
      <path d={body} fill={C.paper} />
      <rect x="30" y="61" width="36" height="30" rx="3" fill="#101a30" clipPath={`url(#${id})`} />
      <path d={body} fill="none" stroke={C.ink} strokeWidth={1.75} strokeLinejoin="round" />
      {happy > 0.5 ? (
        <>
          <path d="M34.9 47.5Q38.5 42.8 42.1 47.5" {...face} />
          <path d="M53.9 47.5Q57.5 42.8 61.1 47.5" {...face} />
          <path d="M43 53Q48 58.5 53 53" {...face} />
        </>
      ) : (
        <>
          <ellipse cx="38.5" cy="46" rx="3.4" ry={Math.max(0.5, 4 * (1 - blink))} fill={C.ink} />
          <ellipse cx="57.5" cy="46" rx="3.4" ry={Math.max(0.5, 4 * (1 - blink))} fill={C.ink} />
          <path d="M44 53.5Q48 57.1 52 53.5" {...face} />
        </>
      )}
    </svg>
  );
};

/** The deck's title-slide ribbons, drawn on over time. */
export const Ribbons: React.FC<{ start: number; dur?: number; muted?: boolean }> = ({ start, dur = 24, muted }) => {
  const frame = useCurrentFrame();
  const paths: [string, string, number, number][] = [
    ["M 500 1090 C 1000 960, 1500 760, 1930 420", C.lilac, 5, 0],
    ["M 700 1120 C 1150 980, 1600 760, 1940 480", C.purple, 36, 2],
    ["M 900 1110 C 1300 1000, 1650 780, 1940 440", C.coral, 22, 4],
    ["M 1050 1110 C 1450 980, 1730 800, 1940 620", "#ffb1b9", 4, 6],
    ["M 1250 1120 C 1550 1040, 1780 920, 1940 800", "#cdbfff", 14, 8],
  ];
  return (
    <svg width={1920} height={1080} style={{ position: "absolute", inset: 0, opacity: muted ? 0.5 : 1 }}>
      {paths.map(([d, color, width, delay]) => (
        <path key={d} d={d} fill="none" stroke={color} strokeWidth={width} strokeLinecap="round" pathLength={1} strokeDasharray={1}
          strokeDashoffset={interpolate(frame, [start + delay, start + delay + dur], [1, 0], { ...clamp, easing: easeOut })} />
      ))}
    </svg>
  );
};

export const Small: React.FC<{ children: React.ReactNode; color?: string; style?: React.CSSProperties }> = ({ children, color = C.ink, style }) => (
  <div style={{ fontSize: 22, fontWeight: 500, color, opacity: 0.62, letterSpacing: 0.1, ...style }}>{children}</div>
);
