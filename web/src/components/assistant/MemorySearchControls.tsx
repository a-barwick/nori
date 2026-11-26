"use client";

import { useEffect, useState } from "react";
import { Search, X } from "lucide-react";
import { useResource } from "@/lib/assistant/useResource";
import type { MemorySearchResponse } from "@/lib/contracts/assistant";
import { cn } from "@/lib/utils";
import { inputClass } from "./ui";

type SearchLayer = "all" | "log" | "core";

export function useMemorySearch(initialQuery: string) {
  const [query, setQuery] = useState(initialQuery);
  const [settled, setSettled] = useState(initialQuery.trim());
  const [layer, setLayer] = useState<SearchLayer>("all");
  useEffect(() => {
    const timer = setTimeout(() => setSettled(query.trim()), 250);
    return () => clearTimeout(timer);
  }, [query]);
  const path = settled ? `/memory/search?q=${encodeURIComponent(settled)}${layer === "all" ? "" : "&layer=" + layer}&limit=20` : null;
  const search = useResource<MemorySearchResponse>(path, { poll: 0, versioned: false });
  const clearSearch = () => { setQuery(""); setSettled(""); };
  return { query, setQuery, settled, layer, setLayer, search, clearSearch };
}

export function MemorySearchControls({ query, setQuery, layer, setLayer, clearSearch }: {
  query: string;
  setQuery: (query: string) => void;
  layer: SearchLayer;
  setLayer: (layer: SearchLayer) => void;
  clearSearch: () => void;
}) {
  return (
      <div className="flex flex-col gap-2 sm:flex-row sm:items-center">
        <label className="relative block flex-1">
          <span className="sr-only">Search memory</span>
          <Search size={17} className="pointer-events-none absolute top-1/2 left-3.5 -translate-y-1/2 text-ink-3" aria-hidden />
          <input
            type="search"
            value={query}
            onChange={(event) => setQuery(event.target.value)}
            placeholder="Search daily logs and core memories…"
            className={cn(inputClass, "h-11 pr-9 pl-10")}
          />
          {query && (
            <button
              type="button"
              aria-label="Clear search"
              onClick={clearSearch}
              className="absolute top-1/2 right-2.5 -translate-y-1/2 rounded-full p-1 text-ink-3 hover:bg-fill-1 hover:text-ink"
            >
              <X size={15} aria-hidden />
            </button>
          )}
        </label>
        <div className="flex shrink-0 items-center gap-1" role="group" aria-label="Search layer">
          {(
            [
              { id: "all", label: "All" },
              { id: "log", label: "Logs" },
              { id: "core", label: "Core" },
            ] as const
          ).map((option) => (
            <button
              key={option.id}
              type="button"
              aria-pressed={layer === option.id}
              onClick={() => setLayer(option.id)}
              className={cn(
                "h-9 rounded-full border px-3 text-meta font-medium",
                layer === option.id
                  ? "border-chip-selected-border bg-chip-selected-bg text-chip-selected-fg"
                  : "border-transparent bg-fill-1 text-ink-2 hover:text-ink",
              )}
            >
              {option.label}
            </button>
          ))}
        </div>
      </div>
  );
}
