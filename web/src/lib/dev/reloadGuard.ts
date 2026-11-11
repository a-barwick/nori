/**
 * Development only. Next's dev client reloads a page when its dev server says so (a new server, a rebuild after a
 * runtime error, an update it can't apply). Twice on 3213 a tab left open across a stack restart reloaded several
 * times a second until the web process was restarted again, and the cause could not be reproduced elsewhere.
 *
 * This guard counts full page loads per tab. After 5 within 15 s it holds back the dev-server messages that can
 * reload that tab (the page still renders; live editing pauses), and it writes one line with the messages and errors seen just before each
 * reload. Next forwards browser errors to the dev server's terminal, so the line lands in the web log. A reload by
 * hand after 15 s quiet brings live reload back.
 */
const KEY = "nori-dev-reload-guard";
const WINDOW_MS = 15_000;
const LIMIT = 5;
// Messages that can make Next's dev client reload the page. Everything else (React's debug chunks, build status,
// error state) still flows, because the page needs some of it to render at all.
const RELOADING = new Set(["reloadPage", "serverComponentChanges", "staticParamsChanged", "turbopack-message", "clientChanges", "serverOnlyChanges", "middlewareChanges"]);
// On a reconnect these compare the server's session id and build hash with the previous ones and reload on a change.
const RELOADING_ON_RECONNECT = new Set(["turbopack-connected", "sync"]);

type Saved = { loads: number[]; before: string[][] };

function read(): Saved {
  try {
    const value = JSON.parse(sessionStorage.getItem(KEY) || "null") as Saved | null;
    if (value && Array.isArray(value.loads) && Array.isArray(value.before)) return value;
  } catch { /* storage unavailable or corrupt: start over */ }
  return { loads: [], before: [] };
}

function write(value: Saved) {
  try { sessionStorage.setItem(KEY, JSON.stringify(value)); } catch { /* storage unavailable */ }
}

export function installReloadGuard() {
  if (typeof window === "undefined" || !("WebSocket" in window)) return;
  const now = Date.now();
  const saved = read();
  const loads = [...saved.loads.filter((at) => now - at < WINDOW_MS), now];
  const paused = loads.length >= LIMIT;
  const seen: string[] = [];
  const note = (entry: string) => { seen.push(entry.slice(0, 120)); if (seen.length > 12) seen.shift(); };
  const store = () => write({ loads, before: [...saved.before, [...seen]].slice(-LIMIT) });
  store();

  if (paused) {
    const navigation = (performance.getEntriesByType("navigation")[0] as PerformanceNavigationTiming | undefined)?.type ?? "unknown";
    const line = `[nori dev] ${location.pathname} loaded ${loads.length} times in ${WINDOW_MS / 1000} s (navigation: ${navigation}). Live reload is paused for this tab; reload it by hand after ${WINDOW_MS / 1000} s to resume. Dev-server messages and errors before each reload, oldest first: ${saved.before.map((list) => `[${list.join(" > ") || "none"}]`).join(" ")}`;
    // Next forwards uncaught browser errors (not console.error) to the dev server's terminal, so the web log has it.
    setTimeout(() => { throw new Error(line); });
  }

  addEventListener("error", (event) => note(`error: ${event.message}`));
  addEventListener("unhandledrejection", (event) => note(`rejection: ${event.reason instanceof Error ? `${event.reason.name}: ${event.reason.message}` : String(event.reason)}`));
  addEventListener("pagehide", store);

  let sockets = 0;
  const Native = window.WebSocket;
  class GuardedSocket extends Native {
    readonly #hmr: boolean;
    readonly #reconnect: boolean;
    constructor(url: string | URL, protocols?: string | string[]) {
      super(url, protocols);
      this.#hmr = String(url).includes("/_next/");
      this.#reconnect = this.#hmr && sockets++ > 0;
      if (this.#hmr) {
        this.addEventListener("open", () => note("hmr: open"));
        this.addEventListener("close", (event) => note(`hmr: close ${event.code}`));
      }
    }
    get onmessage() { return super.onmessage; }
    set onmessage(handler: ((this: WebSocket, event: MessageEvent) => unknown) | null) {
      if (!this.#hmr || !handler) { super.onmessage = handler; return; }
      super.onmessage = (event: MessageEvent) => {
        let kind = "binary";
        if (typeof event.data === "string") {
          try { kind = String((JSON.parse(event.data) as { type?: unknown }).type ?? "unknown"); } catch { kind = "text"; }
        }
        const blocked = paused && (RELOADING.has(kind) || this.#reconnect && RELOADING_ON_RECONNECT.has(kind));
        note(`hmr: ${kind}${blocked ? " (held)" : ""}`);
        if (!blocked) handler.call(this, event);
      };
    }
  }
  window.WebSocket = GuardedSocket;
}
