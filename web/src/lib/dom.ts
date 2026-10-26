/**
 * Scrolls the nearest scrollable ancestor so `el` is in view, and nothing
 * else. Element.scrollIntoView would also scroll the document when the inner
 * container runs out of room, shifting the whole shell.
 */
export function scrollWithin(el: HTMLElement, block: "start" | "center" = "center", topInset = 72) {
  let c = el.parentElement;
  while (c && !(c.scrollHeight > c.clientHeight && /(auto|scroll)/.test(getComputedStyle(c).overflowY))) c = c.parentElement;
  if (!c) return;
  const cr = c.getBoundingClientRect();
  const er = el.getBoundingClientRect();
  const offset = er.top - cr.top + c.scrollTop;
  const top = block === "start" ? offset - topInset : offset - (c.clientHeight - er.height) / 2;
  c.scrollTo({ top: Math.max(0, top), behavior: "smooth" });
}
