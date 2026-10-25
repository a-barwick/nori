"use client";

import { AudioLines, FileText } from "lucide-react";
import type { Artifact } from "@/lib/types";
import { cn } from "@/lib/utils";
import { Markdown } from "./Markdown";

/** Drawn 4:3 previews, near-monochrome, so the Library reads as one system. */

const Line = ({ w, className }: { w: string; className?: string }) => (
  <span className={cn("block h-1.5 rounded-full bg-hairline-strong", className)} style={{ width: w }} />
);

function Doc({ list }: { list?: boolean }) {
  return (
    <div className="absolute inset-[14%_18%_0] rounded-t-[8px] bg-surface p-[9%] shadow-[0_0_0_1px_var(--hairline)]">
      <span className="mb-[10%] block h-2 w-1/2 rounded-full bg-ink/70" />
      <div className="flex flex-col gap-[7px]">
        {(list ? ["86%", "72%", "80%", "64%", "76%", "58%"] : ["92%", "88%", "95%", "70%", "90%", "84%"]).map((w, i) => (
          <span key={i} className="flex items-center gap-1.5">
            {list && <span className="size-1.5 shrink-0 rounded-[2px] shadow-[inset_0_0_0_1px_var(--hairline-strong)]" />}
            <Line w={w} />
          </span>
        ))}
      </div>
    </div>
  );
}

function Table() {
  return (
    <div className="absolute inset-[14%_12%_0] rounded-t-[8px] bg-surface p-[8%] shadow-[0_0_0_1px_var(--hairline)]">
      <span className="mb-[9%] block h-2 w-2/5 rounded-full bg-ink/70" />
      {[0, 1, 2, 3, 4].map((r) => (
        <div key={r} className="grid grid-cols-3 gap-2 border-b border-hairline py-[5px] last:border-0">
          <Line w="70%" className={r === 0 ? "bg-ink-3/60" : ""} />
          <Line w="50%" className={r === 2 ? "bg-accent-signal/70" : ""} />
          <Line w="60%" />
        </div>
      ))}
    </div>
  );
}

function Dash() {
  return (
    <div className="absolute inset-[12%] grid grid-cols-3 grid-rows-2 gap-[6%]">
      <div className="col-span-2 rounded-[8px] bg-surface p-[8%] shadow-[0_0_0_1px_var(--hairline)]">
        <span className="block h-1.5 w-1/3 rounded-full bg-hairline-strong" />
        <svg viewBox="0 0 100 40" className="mt-[8%] w-full" preserveAspectRatio="none" aria-hidden>
          <polyline points="0,32 15,28 30,30 45,18 60,22 75,10 100,14" fill="none" stroke="var(--accent-signal)" strokeWidth="2.5" strokeLinejoin="round" strokeLinecap="round" />
        </svg>
      </div>
      <div className="grid place-items-center rounded-[8px] bg-surface shadow-[0_0_0_1px_var(--hairline)]">
        <span className="block h-3 w-1/2 rounded-full bg-ink/70" />
      </div>
      {[0, 1, 2].map((i) => (
        <div key={i} className="flex items-end gap-[8%] rounded-[8px] bg-surface p-[10%] shadow-[0_0_0_1px_var(--hairline)]">
          {[60, 85, 45, 70].map((h, j) => (
            <span key={j} className="w-full rounded-[2px] bg-hairline-strong" style={{ height: `${(h * (i + 2)) / 4}%` }} />
          ))}
        </div>
      ))}
    </div>
  );
}

function Page() {
  return (
    <div className="absolute inset-[12%_14%_0] overflow-hidden rounded-t-[8px] bg-surface shadow-[0_0_0_1px_var(--hairline)]">
      <div className="h-[38%] bg-fill-2" />
      <div className="flex flex-col gap-[6px] p-[8%]">
        <span className="mb-1 block h-2 w-3/5 rounded-full bg-ink/70" />
        <Line w="90%" />
        <Line w="82%" />
        <Line w="66%" />
      </div>
    </div>
  );
}

function Audio({ durationMs }: { durationMs?: number }) {
  return (
    <div className="absolute inset-0 grid place-items-center">
      <div className="flex items-center gap-[3px]">
        {[30, 55, 40, 75, 50, 90, 65, 40, 70, 45, 80, 35, 60, 30].map((h, i) => (
          <span key={i} className="w-[3px] rounded-full bg-ink-3/70" style={{ height: h * 0.5 }} />
        ))}
      </div>
      {durationMs && <span className="absolute bottom-3 left-3 font-mono text-mono-data text-ink-2">{Math.round(durationMs / 1000)}s</span>}
      <AudioLines size={20} strokeWidth={1.5} className="absolute right-3 bottom-3 text-ink-3" aria-hidden />
    </div>
  );
}

function FileThumb({ label }: { label?: string }) {
  return (
    <div className="absolute inset-0 grid place-items-center">
      <div className="relative grid h-[58%] aspect-[3/4] place-items-center rounded-[8px] bg-surface shadow-[0_0_0_1px_var(--hairline)]">
        <FileText size={26} strokeWidth={1.5} className="text-ink-3" aria-hidden />
        <span className="absolute bottom-2 font-mono text-mono-label text-ink-3 uppercase">pdf</span>
      </div>
      {label && <span className="absolute bottom-3 left-3 font-mono text-mono-data text-ink-2">{label}</span>}
    </div>
  );
}

/** The real thing, shrunk: the document's own title and opening lines on a sheet. */
function Sheet({ title, body }: { title: string; body: string }) {
  return (
    <div className="absolute inset-[10%_12%_0] overflow-hidden rounded-t-[8px] bg-surface shadow-[0_0_0_1px_var(--hairline)]">
      <div className="pointer-events-none p-[22px] select-none" style={{ zoom: 0.42 }} aria-hidden>
        <div className="mb-3 text-title font-medium text-ink">{title}</div>
        <Markdown variant="page">{body.slice(0, 1600)}</Markdown>
      </div>
      <div className="absolute inset-x-0 bottom-0 h-1/3 bg-gradient-to-b from-transparent to-surface" />
    </div>
  );
}

const SHEETS = new Set(["doc", "list", "table", "page", undefined]);

export function Thumbnail({ artifact, className }: { artifact: Artifact; className?: string }) {
  return (
    <div className={cn("relative aspect-[4/3] overflow-hidden bg-fill-1", className)}>
      {artifact.body && SHEETS.has(artifact.thumb) ? (
        <Sheet title={artifact.title} body={artifact.body} />
      ) : artifact.thumb === "image" && artifact.src ? (
        // eslint-disable-next-line @next/next/no-img-element
        <img src={artifact.src} alt="" className="absolute inset-0 size-full object-cover" draggable={false} />
      ) : artifact.thumb === "table" ? (
        <Table />
      ) : artifact.thumb === "dash" ? (
        <Dash />
      ) : artifact.thumb === "page" ? (
        <Page />
      ) : artifact.thumb === "audio" ? (
        <Audio durationMs={artifact.durationMs} />
      ) : artifact.thumb === "file" ? (
        <FileThumb label={artifact.sizeLabel} />
      ) : (
        <Doc list={artifact.thumb === "list"} />
      )}
    </div>
  );
}
