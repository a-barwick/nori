"use client";

import { useEffect, useId, useRef, useState } from "react";
import { cn } from "@/lib/utils";
import type { MarkState } from "@/lib/types";


const RICE = "M48 11C54.5 11 59.2 14.3 62.6 20L84.6 58.4C91.5 70.5 84.6 85 70.6 85H25.4C11.4 85 4.5 70.5 11.4 58.4L33.4 20C36.8 14.3 41.5 11 48 11Z";
const FACE = { stroke: "var(--mark-ink)", strokeWidth: 2.6, strokeLinecap: "round" as const, fill: "none" };

interface Face {
  eyes: [number, number];
  y: number;
  mouthY: number;
}

const FACE_POS: Face = { eyes: [38.5, 57.5], y: 46, mouthY: 53.5 };

function OnigiriBody(clip: string) {
  return (
    <>
      <defs>
        <clipPath id={clip}>
          <path d={RICE} />
        </clipPath>
      </defs>
      <path d={RICE} fill="var(--mark-body)" />
      <rect x="30" y="61" width="36" height="30" rx="3" fill="var(--mark-seaweed)" clipPath={`url(#${clip})`} />
      <path
        d={RICE}
        fill="none"
        stroke="var(--mark-edge)"
        strokeLinejoin="round"
        style={{ strokeWidth: "var(--mark-edge-width, 1.75)" }}
      />
    </>
  );
}

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
  watch = false,
}: {
  size?: number;
  state?: MarkState;
  watch?: boolean;
}) {
  const s = state ?? "idle";
  const clip = useId().replace(/:/g, "");
  const blink = useBlink(s === "idle" || s === "attentive");
  const { svg: svgRef, look: lookRef } = useLook(watch && s === "idle", watch && s === "attentive");
  const face = FACE_POS;
  const [lx, rx] = face.eyes;

  return (
    <svg
      ref={svgRef}
      width={size}
      height={size}
      viewBox="0 0 96 96"
      className="mark shrink-0 overflow-visible"
      data-state={s}
      data-blink={blink}
      aria-hidden
    >
      <g className="mark-body-group">
        {OnigiriBody(`clip-${clip}`)}
        <g ref={lookRef} className="mark-look">
          <g className="mark-eyes">
            <ellipse className="mark-eye" cx={lx} cy={face.y} rx="3.4" ry="4" fill="var(--mark-ink)" />
            <ellipse className="mark-eye" cx={rx} cy={face.y} rx="3.4" ry="4" fill="var(--mark-ink)" />
          </g>
        </g>
        <path d={`M44 ${face.mouthY}Q48 ${face.mouthY + 3.6} 52 ${face.mouthY}`} {...FACE} />
      </g>
    </svg>
  );
}

export function NoriTabIcon({ active = false, size = 18 }: { active?: boolean; size?: number }) {
  const id = useId().replace(/:/g, "");
  if (active) {
    return (
      <svg width={size} height={size} viewBox="0 0 96 96" className="shrink-0" aria-hidden>
        <defs>
          <clipPath id={`tab-clip-${id}`}>
            <path d={RICE} />
          </clipPath>
          <mask id={`tab-mask-${id}`}>
            <rect width="96" height="96" fill="white" />
            <ellipse cx="38.5" cy="45.5" rx="3.8" ry="4.4" fill="black" />
            <ellipse cx="57.5" cy="45.5" rx="3.8" ry="4.4" fill="black" />
            <path
              d="M43.5 53.5Q48 57.6 52.5 53.5"
              fill="none"
              stroke="black"
              strokeWidth="4.2"
              strokeLinecap="round"
            />
            <g clipPath={`url(#tab-clip-${id})`}>
              <rect
                x="30"
                y="61"
                width="36"
                height="30"
                rx="3"
                fill="none"
                stroke="black"
                strokeWidth="4.2"
              />
            </g>
          </mask>
        </defs>
        <path d={RICE} fill="currentColor" mask={`url(#tab-mask-${id})`} />
      </svg>
    );
  }
  return (
    <svg width={size} height={size} viewBox="0 0 96 96" className="shrink-0" aria-hidden>
      <defs>
        <clipPath id={`tab-outline-clip-${id}`}>
          <path d={RICE} />
        </clipPath>
      </defs>
      <rect x="30" y="61" width="36" height="30" rx="3" fill="currentColor" clipPath={`url(#tab-outline-clip-${id})`} />
      <path d={RICE} fill="none" stroke="currentColor" strokeWidth="6" strokeLinejoin="round" />
      <ellipse cx="38.5" cy="45.5" rx="3.8" ry="4.4" fill="currentColor" />
      <ellipse cx="57.5" cy="45.5" rx="3.8" ry="4.4" fill="currentColor" />
      <path
        d="M43.5 53.5Q48 57.6 52.5 53.5"
        fill="none"
        stroke="currentColor"
        strokeWidth="4"
        strokeLinecap="round"
      />
    </svg>
  );
}

export function Medallion({
  size,
  state,
  className,
  watch,
}: {
  size: number;
  state?: MarkState;
  className?: string;
  watch?: boolean;
}) {
  return (
    <span
      className={cn("relative inline-grid shrink-0 place-items-center", className)}
      style={{ width: size, height: size }}
    >
      <NoriMark size={Math.round(size * 0.9)} state={state} watch={watch} />
    </span>
  );
}
