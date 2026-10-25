"use client";

import { useEffect, useId, useRef, useState } from "react";
import { useNori } from "@/lib/store";
import { cn } from "@/lib/utils";
import type { MarkLook, MarkState } from "@/lib/types";

/**
 * Nori's mark: at most five simple shapes, ink outline (1.75 at 96px,
 * scaled), two-tone olive fill. Reads as a silhouette at 20px.
 */

const INK = { stroke: "var(--mark-ink)", strokeWidth: 1.75, strokeLinejoin: "round" as const };
const FACE = { stroke: "var(--mark-ink)", strokeWidth: 2.6, strokeLinecap: "round" as const, fill: "none" };

interface Face {
  eyes: [number, number];
  y: number;
  mouthY: number;
}

const LOOKS: Record<MarkLook, { face: Face; body: (clip: string) => React.ReactNode }> = {
  onigiri: {
    face: { eyes: [38.5, 57.5], y: 46, mouthY: 53.5 },
    body: (clip) => (
      <>
        <defs>
          <clipPath id={clip}>
            <path d="M48 11C54.5 11 59.2 14.3 62.6 20L84.6 58.4C91.5 70.5 84.6 85 70.6 85H25.4C11.4 85 4.5 70.5 11.4 58.4L33.4 20C36.8 14.3 41.5 11 48 11Z" />
          </clipPath>
        </defs>
        <path
          d="M48 11C54.5 11 59.2 14.3 62.6 20L84.6 58.4C91.5 70.5 84.6 85 70.6 85H25.4C11.4 85 4.5 70.5 11.4 58.4L33.4 20C36.8 14.3 41.5 11 48 11Z"
          fill="var(--mark-fill-soft)"
        />
        <rect x="30" y="61" width="36" height="30" rx="3" fill="var(--mark-fill)" clipPath={`url(#${clip})`} />
        <path
          d="M48 11C54.5 11 59.2 14.3 62.6 20L84.6 58.4C91.5 70.5 84.6 85 70.6 85H25.4C11.4 85 4.5 70.5 11.4 58.4L33.4 20C36.8 14.3 41.5 11 48 11Z"
          fill="none"
          {...INK}
        />
      </>
    ),
  },
  sprout: {
    face: { eyes: [39, 57], y: 58, mouthY: 66 },
    body: () => (
      <>
        <path
          d="M48 34C37 22 27 22 21 25C25 33 34 38 48 36C62 38 71 31 75 22C68 18 57 20 48 34Z"
          fill="var(--mark-fill)"
          {...INK}
        />
        <path d="M48 35C68 35 81 47 81 61C81 75 67 86 48 86C29 86 15 75 15 61C15 47 28 35 48 35Z" fill="var(--mark-fill-soft)" {...INK} />
      </>
    ),
  },
  pebble: {
    face: { eyes: [39.5, 56.5], y: 54, mouthY: 61.5 },
    body: () => (
      <>
        <path d="M48 16C71 16 86 29 86 52C86 73 71 86 48 86C25 86 10 73 10 52C10 29 25 16 48 16Z" fill="var(--mark-fill)" {...INK} />
        <ellipse cx="48" cy="57" rx="24" ry="18" fill="var(--mark-fill-soft)" />
      </>
    ),
  },
};

function useBlink(enabled: boolean) {
  const [blink, setBlink] = useState(false);
  useEffect(() => {
    if (!enabled) return;
    if (window.matchMedia("(prefers-reduced-motion: reduce)").matches) return;
    let t: ReturnType<typeof setTimeout>;
    const schedule = () => {
      t = setTimeout(() => {
        setBlink(true);
        t = setTimeout(() => {
          setBlink(false);
          schedule();
        }, 140);
      }, 6000 + Math.random() * 3000);
    };
    schedule();
    return () => clearTimeout(t);
  }, [enabled]);
  return blink;
}

/** The eyes follow your pointer, a few pixels at most; they look down at the composer while you type. */
function useLook(enabled: boolean, attentive: boolean) {
  const svg = useRef<SVGSVGElement>(null);
  const look = useRef<SVGGElement>(null);
  useEffect(() => {
    const g = look.current;
    if (!g) return;
    const aim = (x: number, y: number) => {
      g.style.transform = `translate(${x.toFixed(2)}px, ${y.toFixed(2)}px)`;
    };
    if (attentive || !enabled || window.matchMedia("(prefers-reduced-motion: reduce)").matches) {
      aim(0, attentive ? 2.6 : 0);
      return;
    }
    let frame = 0;
    let idle: ReturnType<typeof setTimeout>;
    const onMove = (e: PointerEvent) => {
      cancelAnimationFrame(frame);
      frame = requestAnimationFrame(() => {
        const r = svg.current?.getBoundingClientRect();
        if (!r) return;
        const dx = e.clientX - (r.left + r.width / 2);
        const dy = e.clientY - (r.top + r.height / 2);
        const d = Math.hypot(dx, dy) || 1;
        const reach = Math.min(1, d / 320) * 3.2;
        aim((dx / d) * reach, (dy / d) * reach * 0.8);
      });
      clearTimeout(idle);
      idle = setTimeout(() => aim(0, 0), 4000);
    };
    window.addEventListener("pointermove", onMove, { passive: true });
    return () => {
      window.removeEventListener("pointermove", onMove);
      cancelAnimationFrame(frame);
      clearTimeout(idle);
      aim(0, 0);
    };
  }, [enabled, attentive]);
  return { svg, look };
}

export function NoriMark({
  size = 32,
  state,
  look,
  className,
  title,
  watch = false,
}: {
  size?: number;
  state?: MarkState;
  look?: MarkLook;
  className?: string;
  title?: string;
  /** Follow the pointer with its eyes (the presence medallion only). */
  watch?: boolean;
}) {
  const storeState = useNori((s) => s.markState);
  const storeLook = useNori((s) => s.identity.look);
  const s = state ?? storeState;
  const l = look ?? storeLook;
  const clip = useId().replace(/:/g, "");
  const blink = useBlink(s === "idle" || s === "attentive" || s === "writing");
  const { svg: svgRef, look: lookRef } = useLook(watch && s === "idle", watch && s === "attentive");
  const { face, body } = LOOKS[l];
  const [lx, rx] = face.eyes;
  const happy = s === "pleased";

  return (
    <svg
      ref={svgRef}
      width={size}
      height={size}
      viewBox="0 0 96 96"
      className={cn("mark shrink-0 overflow-visible", className)}
      data-state={s}
      data-blink={blink}
      role={title ? "img" : undefined}
      aria-label={title}
      aria-hidden={title ? undefined : true}
    >
      <g className="mark-body-group">
        {body(`clip-${clip}`)}
        <g ref={lookRef} className="mark-look">
        <g className="mark-eyes">
          {happy ? (
            <>
              <path d={`M${lx - 3.6} ${face.y + 1.5}Q${lx} ${face.y - 3.2} ${lx + 3.6} ${face.y + 1.5}`} {...FACE} />
              <path d={`M${rx - 3.6} ${face.y + 1.5}Q${rx} ${face.y - 3.2} ${rx + 3.6} ${face.y + 1.5}`} {...FACE} />
            </>
          ) : (
            <>
              <ellipse className="mark-eye" cx={lx} cy={face.y} rx="3.4" ry="4" fill="var(--mark-ink)" />
              <ellipse className="mark-eye" cx={rx} cy={face.y} rx="3.4" ry="4" fill="var(--mark-ink)" />
            </>
          )}
        </g>
        </g>
        <path
          d={happy ? `M43 ${face.mouthY - 0.5}Q48 ${face.mouthY + 5} 53 ${face.mouthY - 0.5}` : `M44 ${face.mouthY}Q48 ${face.mouthY + 3.6} 52 ${face.mouthY}`}
          {...FACE}
        />
      </g>
    </svg>
  );
}

/** Avatar contexts: the mark sits in a paper circle with a hairline ring. */
export function Medallion({
  size,
  state,
  look,
  className,
  children,
  watch,
}: {
  size: number;
  state?: MarkState;
  look?: MarkLook;
  className?: string;
  children?: React.ReactNode;
  watch?: boolean;
}) {
  return (
    <span
      className={cn("relative inline-grid shrink-0 place-items-center rounded-full bg-[var(--mark-paper)]", className)}
      style={{ width: size, height: size, boxShadow: "inset 0 0 0 1px var(--hairline)" }}
    >
      <NoriMark size={Math.round(size * 0.74)} state={state} look={look} watch={watch} />
      {children}
    </span>
  );
}
