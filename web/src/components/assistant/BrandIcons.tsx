"use client";

import type { ReactNode } from "react";
import { KeyRound, Monitor, Plug } from "lucide-react";
import { cn } from "@/lib/utils";

export function BrandIconWell({
  children,
  brand = true,
  tone = "neutral",
  className,
}: {
  children: ReactNode;
  brand?: boolean;
  tone?: "neutral" | "accent" | "danger";
  className?: string;
}) {
  return (
    <span
      className={cn(
        "grid size-9 shrink-0 place-items-center rounded-[10px]",
        brand
          ? "border border-card-border bg-white dark:border-white/10 dark:bg-[#181820]"
          : tone === "accent"
            ? "bg-accent-subtle text-accent-text"
            : tone === "danger"
              ? "bg-danger-bg text-danger"
              : "bg-fill-1 text-ink-2",
        className,
      )}
    >
      {children}
    </span>
  );
}

export function GoogleCalendarIcon({ size = 20 }: { size?: number }) {
  return (
    <svg width={size} height={size} viewBox="0 0 24 24" aria-hidden>
      <path fill="#4285F4" d="M18 4H6a2 2 0 0 0-2 2v12a2 2 0 0 0 2 2h9l5-5V6a2 2 0 0 0-2-2Z" />
      <path fill="#FFFFFF" d="M7.5 8.5h9v7h-9z" />
      <path fill="#EA4335" d="M15 20v-5h5z" />
      <path fill="#34A853" d="M6 20h9v-2.5H6z" />
      <path fill="#FBBC04" d="M17.5 8.5H20V15h-2.5z" />
      <path
        fill="#1967D2"
        d="M10.4 14.3c-.6 0-1.1-.2-1.4-.5l.6-.7c.2.2.5.3.8.3.4 0 .7-.2.7-.5s-.3-.5-.8-.5h-.4v-.8h.4c.4 0 .7-.2.7-.5s-.2-.4-.6-.4c-.3 0-.5.1-.7.3l-.6-.6c.4-.4.9-.6 1.5-.6 1 0 1.6.5 1.6 1.2 0 .5-.3.8-.7 1 .5.2.8.6.8 1.1 0 .8-.7 1.3-1.9 1.3Zm3.6-.1v-3.2l-.7.5-.5-.7 1.4-1h.8v4.4h-1Z"
      />
    </svg>
  );
}

export function GoogleDriveIcon({ size = 20 }: { size?: number }) {
  return (
    <svg width={size} height={size} viewBox="0 0 24 24" aria-hidden>
      <path fill="#0066DA" d="M4.6 19.5 7.4 14.7 10.2 19.5H4.6Z" />
      <path fill="#00AC47" d="M8.7 4.5h6.6l-3.3 5.7-3.3 5.7-3.3-5.7 3.3-5.7Z" />
      <path fill="#EA4335" d="M19.4 19.5h-5.6l2.8-4.8 2.8 4.8Z" />
      <path fill="#00832D" d="M8.7 15.9 5.4 10.2 2.1 15.9l2.5 3.6 4.1-3.6Z" />
      <path fill="#2684FC" d="M13.8 19.5H7.4l2.8-4.8h9.2l-2.8 4.8h-2.8Z" />
      <path fill="#FFBA00" d="M15.3 4.5H8.7l6.6 11.4h6.6L15.3 4.5Z" />
    </svg>
  );
}

export function GmailIcon({ size = 20 }: { size?: number }) {
  return (
    <svg width={size} height={size} viewBox="0 0 24 24" aria-hidden>
      <path fill="#4285F4" d="M4 18.5h2.8v-6.8L3 8.8v8.7c0 .6.4 1 1 1Z" />
      <path fill="#34A853" d="M17.2 18.5H20c.6 0 1-.4 1-1V8.8l-3.8 2.9v6.8Z" />
      <path fill="#FBBC04" d="M17.2 6.8v4.9L21 8.8V7.2c0-1.2-1.4-1.9-2.4-1.2l-1.4.8Z" />
      <path fill="#EA4335" d="M6.8 11.7V6.8L12 10.7l5.2-3.9v4.9L12 15.6l-5.2-3.9Z" />
      <path fill="#C5221F" d="M3 7.2v1.6l3.8 2.9V6.8L5.4 6C4.4 5.3 3 6 3 7.2Z" />
    </svg>
  );
}

export function LinearIcon({ size = 19 }: { size?: number }) {
  return (
    <svg width={size} height={size} viewBox="0 0 24 24" aria-hidden>
      <path
        fill="#5E6AD2"
        d="M2.2 13.4a9.9 9.9 0 0 0 8.4 8.4l-8.4-8.4Zm-.1-2.8 11.3 11.3a9.9 9.9 0 0 0 2-.5L2.6 8.6a9.9 9.9 0 0 0-.5 2Zm1.5-3.6 13.4 13.4c.5-.3 1-.7 1.4-1.1L4.7 5.6c-.4.4-.8.9-1.1 1.4Zm2.4-2.5 13.5 13.5a9.9 9.9 0 0 0-13.5-13.5Z"
      />
    </svg>
  );
}

export function NotionIcon({ size = 19 }: { size?: number }) {
  return (
    <svg width={size} height={size} viewBox="0 0 24 24" className="text-ink" aria-hidden>
      <path
        fill="currentColor"
        d="M4.5 4.2c.9.7 1.2.6 2.9.5l11-.7c.3 0 .1-.3-.1-.4L16.5 2.3c-.6-.4-1.3-.9-2.7-.8L3.4 2.3c-.4 0-.5.3-.3.5l1.4 1.4Zm.7 2.5v13.6c0 .7.4 1 1.2 1l12.4-.7c.8 0 1-.5 1-1.1V6.1c0-.6-.2-.9-.8-.8l-13 .7c-.6.1-.8.4-.8.7Zm12.4.7c.1.4 0 .7-.4.8l-.6.1v10c-.5.3-1 .4-1.4.4-.7 0-.8-.2-1.3-.8l-4-6.3v6.1l1.2.3s0 .7-1 .7l-2.7.2c-.1-.2 0-.6.3-.7l.7-.2V9.3l-1-.1c-.1-.4.1-.9.7-.9l2.9-.2 4.1 6.4V8.8l-1-.1c-.1-.5.2-.8.7-.8l2.9-.5Z"
      />
    </svg>
  );
}

export function AnthropicIcon({ size = 18 }: { size?: number }) {
  return (
    <svg width={size} height={size} viewBox="0 0 24 24" aria-hidden>
      <path fill="#D97757" d="M14.2 4h3.2L22 20h-3.1l-1-3.3h-4.7l-1 3.3H9.1L14.2 4Zm-.1 10h2.9l-1.4-5-1.5 5ZM5.7 4h3L13 20H9.9L5.7 4Z" />
    </svg>
  );
}

export function OpenAIIcon({ size = 19 }: { size?: number }) {
  return (
    <svg width={size} height={size} viewBox="0 0 24 24" className="text-ink" aria-hidden>
      <path
        fill="currentColor"
        d="M21.1 10.1a5.2 5.2 0 0 0-.4-4.3 5.3 5.3 0 0 0-5.7-2.5 5.2 5.2 0 0 0-3.9-1.7 5.3 5.3 0 0 0-5 3.6 5.2 5.2 0 0 0-3.5 2.5 5.3 5.3 0 0 0 .6 6.2 5.2 5.2 0 0 0 .4 4.3 5.3 5.3 0 0 0 5.7 2.5 5.2 5.2 0 0 0 3.9 1.7 5.3 5.3 0 0 0 5-3.6 5.2 5.2 0 0 0 3.5-2.5 5.3 5.3 0 0 0-.6-6.2ZM13.2 20.6a3.9 3.9 0 0 1-2.5-.9l.1-.1 4.2-2.4a.7.7 0 0 0 .3-.6v-5.9l1.8 1v4.8a3.9 3.9 0 0 1-3.9 4.1ZM4.7 17a3.9 3.9 0 0 1-.5-2.6l.1.1 4.2 2.4a.7.7 0 0 0 .7 0l5.1-3v2l-4.2 2.4a3.9 3.9 0 0 1-5.4-1.3ZM3.6 8a3.9 3.9 0 0 1 2.1-1.7v4.9c0 .2.1.5.3.6l5.1 2.9-1.8 1-4.2-2.4A3.9 3.9 0 0 1 3.6 8Zm14.6 3.4-5.1-3 1.8-1 4.2 2.4a3.9 3.9 0 0 1-.6 7.1V12a.7.7 0 0 0-.3-.6Zm1.8-2.6-.1-.1-4.2-2.4a.7.7 0 0 0-.7 0l-5.1 3v-2l4.2-2.4a3.9 3.9 0 0 1 5.9 3.9ZM8.9 12.4l-1.8-1V6.6a3.9 3.9 0 0 1 6.4-3l-.1.1-4.2 2.4a.7.7 0 0 0-.3.6v5.7Zm1-2.1 2.2-1.3 2.2 1.3v2.6l-2.2 1.3-2.2-1.3v-2.6Z"
      />
    </svg>
  );
}

export function GoogleGeminiIcon({ size = 19 }: { size?: number }) {
  return (
    <svg width={size} height={size} viewBox="0 0 24 24" aria-hidden>
      <defs>
        <linearGradient id="nori-gemini-grad" x1="0%" y1="0%" x2="100%" y2="100%">
          <stop offset="0%" stopColor="#4285F4" />
          <stop offset="50%" stopColor="#9B72CB" />
          <stop offset="100%" stopColor="#D96570" />
        </linearGradient>
      </defs>
      <path
        fill="url(#nori-gemini-grad)"
        d="M12 2c.6 5.2 4.8 9.4 10 10-5.2.6-9.4 4.8-10 10-.6-5.2-4.8-9.4-10-10 5.2-.6 9.4-4.8 10-10Z"
      />
    </svg>
  );
}

export function VercelIcon({ size = 16 }: { size?: number }) {
  return (
    <svg width={size} height={size} viewBox="0 0 24 24" className="text-ink" aria-hidden>
      <path fill="currentColor" d="M12 3 22 20H2L12 3Z" />
    </svg>
  );
}

export function ProviderBrandIcon({
  provider,
  model,
  backend,
}: {
  provider?: string;
  model?: string;
  backend?: string;
}) {
  const hay = `${provider ?? ""} ${model ?? ""} ${backend ?? ""}`.toLowerCase();
  if (backend === "home" || hay.includes("home-") || hay.includes("ollama") || hay.includes("inkling") || hay.includes("local")) {
    return (
      <BrandIconWell brand={false}>
        <Monitor size={17} strokeWidth={1.85} aria-hidden />
      </BrandIconWell>
    );
  }
  if (hay.includes("anthropic") || hay.includes("claude")) {
    return <BrandIconWell><AnthropicIcon /></BrandIconWell>;
  }
  if (hay.includes("openai") || /\bgpt|\bo1|\bo3|\bo4/.test(hay)) {
    return <BrandIconWell><OpenAIIcon /></BrandIconWell>;
  }
  if (hay.includes("google") || hay.includes("gemini")) {
    return <BrandIconWell><GoogleGeminiIcon /></BrandIconWell>;
  }
  if (hay.includes("vercel")) {
    return <BrandIconWell><VercelIcon /></BrandIconWell>;
  }
  return (
    <BrandIconWell brand={false}>
      <KeyRound size={17} strokeWidth={1.85} aria-hidden />
    </BrandIconWell>
  );
}

export function AppBrandIcon({
  name,
  endpoint,
  service,
}: {
  name?: string;
  endpoint?: string;
  service?: "calendar" | "drive" | "gmail";
}) {
  if (service === "calendar") return <BrandIconWell><GoogleCalendarIcon /></BrandIconWell>;
  if (service === "drive") return <BrandIconWell><GoogleDriveIcon /></BrandIconWell>;
  if (service === "gmail") return <BrandIconWell><GmailIcon /></BrandIconWell>;

  const hay = `${name ?? ""} ${endpoint ?? ""}`.toLowerCase();
  if (hay.includes("linear")) return <BrandIconWell><LinearIcon /></BrandIconWell>;
  if (hay.includes("notion")) return <BrandIconWell><NotionIcon /></BrandIconWell>;
  if (hay.includes("calendar")) return <BrandIconWell><GoogleCalendarIcon /></BrandIconWell>;
  if (hay.includes("drive")) return <BrandIconWell><GoogleDriveIcon /></BrandIconWell>;
  if (hay.includes("gmail") || hay.includes("mail")) return <BrandIconWell><GmailIcon /></BrandIconWell>;

  return (
    <BrandIconWell brand={false}>
      <Plug size={17} strokeWidth={1.85} aria-hidden />
    </BrandIconWell>
  );
}
