const MIN = 60_000;
const DAY = 24 * 60 * MIN;

export const minutes = (n: number) => n * MIN;

function startOfDay(t: number) {
  const d = new Date(t);
  d.setHours(0, 0, 0, 0);
  return d.getTime();
}

export function clock(t: number) {
  return new Date(t).toLocaleTimeString("en-US", { hour: "numeric", minute: "2-digit" });
}

function daysBetween(a: number, b: number) {
  return Math.round((startOfDay(b) - startOfDay(a)) / DAY);
}

export function separatorLabel(t: number, now = Date.now()) {
  const diff = daysBetween(t, now);
  if (diff === 0) return `Today ${clock(t)}`;
  if (diff === 1) return `Yesterday ${clock(t)}`;
  const date = new Date(t).toLocaleDateString("en-US", { month: "short", day: "numeric" });
  return `${date}, ${clock(t)}`;
}

export function shortDate(t: number) {
  return new Date(t).toLocaleDateString("en-US", { month: "short", day: "numeric" });
}
