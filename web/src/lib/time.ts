const MIN = 60_000;
const HOUR = 60 * MIN;
const DAY = 24 * HOUR;

export const minutes = (n: number) => n * MIN;

function startOfDay(t: number) {
  const d = new Date(t);
  d.setHours(0, 0, 0, 0);
  return d.getTime();
}

/** A timestamp `daysAgo` days back at a local clock time like "9:15". */
export function dayAt(now: number, daysAgo: number, clock: string) {
  const [h, m] = clock.split(":").map(Number);
  const d = new Date(startOfDay(now) - daysAgo * DAY);
  d.setHours(h, m, 0, 0);
  return d.getTime();
}

export function clock(t: number) {
  return new Date(t).toLocaleTimeString("en-US", { hour: "numeric", minute: "2-digit" });
}

export function daysBetween(a: number, b: number) {
  return Math.round((startOfDay(b) - startOfDay(a)) / DAY);
}

/** "Today 9:15 AM", "Yesterday 8:31 AM", "Oct 22, 11:32 AM" */
export function separatorLabel(t: number, now = Date.now()) {
  const diff = daysBetween(t, now);
  if (diff === 0) return `Today ${clock(t)}`;
  if (diff === 1) return `Yesterday ${clock(t)}`;
  const date = new Date(t).toLocaleDateString("en-US", { month: "short", day: "numeric" });
  return `${date}, ${clock(t)}`;
}

/** Day header for grouped lists: "Today", "Yesterday", "Oct 22". */
export function dayLabel(t: number, now = Date.now()) {
  const diff = daysBetween(t, now);
  if (diff === 0) return "Today";
  if (diff === 1) return "Yesterday";
  return new Date(t).toLocaleDateString("en-US", { month: "short", day: "numeric" });
}

export function shortDate(t: number) {
  return new Date(t).toLocaleDateString("en-US", { month: "short", day: "numeric" });
}

/** Card date in mono: 10.10.26 */
export function monoDate(t: number) {
  const d = new Date(t);
  const p = (n: number) => String(n).padStart(2, "0");
  return `${p(d.getMonth() + 1)}.${p(d.getDate())}.${String(d.getFullYear()).slice(2)}`;
}

/** Relative time for rows: "now", "12m", "3h", "Yesterday", "Oct 22" */
export function relative(t: number, now = Date.now()) {
  const d = now - t;
  if (d < MIN) return "now";
  if (d < HOUR) return `${Math.floor(d / MIN)}m`;
  if (daysBetween(t, now) === 0) return `${Math.floor(d / HOUR)}h`;
  if (daysBetween(t, now) === 1) return "Yesterday";
  return shortDate(t);
}

/** When the next run happens: "2:30 PM", "tomorrow 8:00 AM", "Sat 9:30 AM", "Nov 14".
 *  dayOnly drops the clock when the schedule already names it. */
export function nextRun(t: number, now = Date.now(), dayOnly = false) {
  const diff = daysBetween(now, t);
  const at = dayOnly ? "" : ` ${clock(t)}`;
  if (diff === 0) return dayOnly ? "today" : clock(t);
  if (diff === 1) return `tomorrow${at}`;
  if (diff < 7) return `${new Date(t).toLocaleDateString("en-US", { weekday: "short" })}${at}`;
  return shortDate(t);
}

/** Rounds up to the next :00 or :30. */
export function nextHalfHour(now: number) {
  const d = new Date(now);
  d.setSeconds(0, 0);
  const m = d.getMinutes();
  d.setMinutes(m < 30 ? 30 : 60);
  return d.getTime();
}

export function nextWeekday(now: number, weekday: number, clockTime: string) {
  const d = new Date(now);
  const [h, m] = clockTime.split(":").map(Number);
  const add = (weekday - d.getDay() + 7) % 7 || 7;
  d.setDate(d.getDate() + add);
  d.setHours(h, m, 0, 0);
  return d.getTime();
}

export function duration(ms: number) {
  const s = Math.max(0, Math.round(ms / 1000));
  return `${Math.floor(s / 60)}:${String(s % 60).padStart(2, "0")}`;
}

export { MIN, HOUR, DAY };
