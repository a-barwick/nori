import type { CalendarTime, Effect, Trigger } from "@/lib/contracts/assistant";

const rtf = new Intl.RelativeTimeFormat("en", { numeric: "auto" });

/** "in 20 min", "3 hours ago", "yesterday". */
export function relative(iso?: string, now = Date.now()) {
  if (!iso) return "";
  const diff = Date.parse(iso) - now;
  const abs = Math.abs(diff);
  if (abs < 45_000) return diff >= 0 ? "now" : "just now";
  if (abs < 3_600_000) return rtf.format(Math.round(diff / 60_000), "minute");
  if (abs < 86_400_000) return rtf.format(Math.round(diff / 3_600_000), "hour");
  if (abs < 7 * 86_400_000) return rtf.format(Math.round(diff / 86_400_000), "day");
  return when(iso);
}

export function when(iso?: string, timeZone?: string) {
  if (!iso) return "";
  const date = new Date(iso);
  const sameYear = date.getFullYear() === new Date().getFullYear();
  return date.toLocaleString("en-GB", { weekday: "short", day: "numeric", month: "short", ...(sameYear ? {} : { year: "numeric" }), hour: "numeric", minute: "2-digit", timeZone });
}

export function day(iso?: string) {
  if (!iso) return "";
  return new Date(iso).toLocaleDateString("en-GB", { weekday: "short", day: "numeric", month: "short" });
}

export function money(amount?: number) {
  if (amount === undefined) return "";
  return amount < 0.01 && amount > 0 ? "<$0.01" : `$${amount.toFixed(2)}`;
}

export function eventTime(time: CalendarTime) {
  if (time.kind === "all_day") {
    const end = new Date(Date.parse(time.end_date) - 86_400_000).toISOString().slice(0, 10);
    return end === time.start_date ? `${day(time.start_date)} · all day` : `${day(time.start_date)} – ${day(end)}`;
  }
  const start = new Date(time.start);
  const end = new Date(time.end);
  const fmt = (d: Date) => d.toLocaleTimeString("en-GB", { hour: "numeric", minute: "2-digit", timeZone: time.timezone });
  return `${start.toLocaleDateString("en-GB", { weekday: "short", day: "numeric", month: "short", timeZone: time.timezone })}, ${fmt(start)}–${fmt(end)}`;
}

const DAYS: Record<string, string> = { MO: "Mon", TU: "Tue", WE: "Wed", TH: "Thu", FR: "Fri", SA: "Sat", SU: "Sun" };

/** Readable cadence for a trigger. */
export function cadence(trigger: Trigger) {
  if (trigger.kind === "once") return `Once, ${when(trigger.at)}`;
  if (trigger.kind === "event_relative") {
    const minutes = Math.round(Math.abs(trigger.offset_seconds) / 60);
    const span = minutes >= 60 && minutes % 60 === 0 ? `${minutes / 60} h` : `${minutes} min`;
    return `${span} ${trigger.offset_seconds <= 0 ? "before" : "after"} an event`;
  }
  if (trigger.kind === "watch") return `Checks for changes every ${Math.round(trigger.min_interval_seconds / 60)} min or more`;
  const time = trigger.local_start.split("T")[1]?.slice(0, 5) ?? "";
  const rule = Object.fromEntries(trigger.rrule.split(";").map((part) => part.split("=")));
  const days = (rule.BYDAY as string | undefined)?.split(",");
  const weekdays = days?.length === 5 && ["MO", "TU", "WE", "TH", "FR"].every((d) => days.includes(d));
  const freq = rule.FREQ as string;
  const at = time ? ` at ${time}` : "";
  if (freq === "DAILY") return `Every day${at}`;
  if (freq === "WEEKLY") return weekdays ? `Weekdays${at}` : days ? `${days.map((d) => DAYS[d] ?? d).join(", ")}${at}` : `Weekly${at}`;
  if (freq === "MONTHLY") return `Monthly${at}`;
  return `${trigger.rrule}${at}`;
}

export const EFFECT_LABEL: Record<Effect, string> = {
  read: "Read",
  reversible_write: "Change",
  sensitive_send: "Send",
  unknown: "Any action",
  control: "Nori control",
};

export function plural(n: number, one: string, many = `${one}s`) {
  return `${n} ${n === 1 ? one : many}`;
}
