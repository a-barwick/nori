import { cn } from "@/lib/utils";

/** Pages (Ideas, Goals): 760px content, left-aligned in a centered container. */
export function Page({ children, className }: { children: React.ReactNode; className?: string }) {
  return (
    <div className="scrollbar-quiet h-full overflow-y-auto">
      <div className={cn("mx-auto w-full max-w-[856px] px-12 pt-12 pb-24", className)}>{children}</div>
    </div>
  );
}

export function PageTitle({ children, aside }: { children: React.ReactNode; aside?: React.ReactNode }) {
  return (
    <div className="flex items-center justify-between gap-4">
      <h1 className="text-display font-medium text-ink">{children}</h1>
      {aside}
    </div>
  );
}

export function Subtitle({ children }: { children: React.ReactNode }) {
  return <p className="mt-2 max-w-[60ch] text-subtitle text-ink-2">{children}</p>;
}
