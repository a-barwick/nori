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

const MORPH = "artifact";

/** Waits (outside rAF, which pauses during a view transition) until `ready` holds or ~1s passes. */
function until<T>(ready: () => T | null | undefined): Promise<T | null> {
  const start = performance.now();
  return new Promise((resolve) => {
    const check = () => {
      const v = ready();
      if (v || performance.now() - start > 1000) resolve(v ?? null);
      else setTimeout(check, 16);
    };
    check();
  });
}

const canMorph = () =>
  typeof document !== "undefined" && "startViewTransition" in document && !window.matchMedia("(prefers-reduced-motion: reduce)").matches;

/**
 * Grows an artifact's tile into its viewer. `update` opens the viewer; the
 * viewer's body carries `view-transition-name: artifact`, so the browser
 * morphs the tile's box into it.
 */
export function morphOpen(from: HTMLElement | null, update: () => void) {
  if (!from || !canMorph()) return update();
  from.style.viewTransitionName = MORPH;
  const t = document.startViewTransition(async () => {
    from.style.viewTransitionName = "";
    update();
    await until(() => document.querySelector("[data-artifact-viewer]"));
  });
  t.finished.finally(() => (from.style.viewTransitionName = ""));
}

/** The reverse: the viewer shrinks back into the tile with `data-tile={id}`. */
export function morphClose(id: string, update: () => void) {
  if (!canMorph()) return update();
  let tile: HTMLElement | null = null;
  const t = document.startViewTransition(async () => {
    update();
    tile = await until(() => document.querySelector<HTMLElement>(`[data-tile="${id}"]`));
    if (tile) tile.style.viewTransitionName = MORPH;
  });
  t.finished.finally(() => {
    if (tile) tile.style.viewTransitionName = "";
  });
}
