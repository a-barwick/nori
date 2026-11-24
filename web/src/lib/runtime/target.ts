export type Mode = "hosted" | "local";
export type RuntimeTarget = { mode: Mode; base: URL };

const LOOPBACK = new Set(["localhost", "127.0.0.1", "[::1]"]);
// Opt-in for testing on a phone: hosts named in NORI_DEV_ORIGINS (this Mac's LAN address) count as local
// in development. Anyone on that network then acts as the local user, so set it only while testing.
const DEV_ORIGINS = new Set((process.env.NORI_DEV_ORIGINS ?? "").split(",").map((origin) => origin.trim()).filter(Boolean));

export function localDevelopment(headers: Headers) {
  if (process.env.NODE_ENV !== "development") return false;
  try {
    const host = new URL(`http://${headers.get("host")}`).hostname;
    return LOOPBACK.has(host) || DEV_ORIGINS.has(host);
  } catch {
    return false;
  }
}

export function runtimeMode(headers: Headers): Mode {
  return localDevelopment(headers) && process.env.NORI_LOCAL_AGENT_URL ? "local" : "hosted";
}

export function runtimeTarget(mode: Mode): RuntimeTarget | Response {
  const configured = mode === "local" ? process.env.NORI_LOCAL_AGENT_URL : process.env.NORI_RUNTIME_URL;
  if (!configured) {
    return mode === "local"
      ? Response.json({ error: "Not found." }, { status: 404 })
      : Response.json({ error: "Nori's runtime is not configured yet." }, { status: 503 });
  }
  try {
    // Keep Agent Runtime's invoke prefix when resolving the scoped API path.
    const base = new URL(configured.endsWith("/") ? configured : `${configured}/`);
    const valid = mode === "local"
      ? base.protocol === "http:" && LOOPBACK.has(base.hostname) && base.pathname === "/"
      : base.protocol === "https:";
    if (!valid || base.username || base.password || base.search || base.hash) throw new Error();
    return { mode, base };
  } catch {
    const error = mode === "local" ? "NORI_LOCAL_AGENT_URL must be a loopback HTTP origin." : "Nori's runtime URL is invalid.";
    return Response.json({ error }, { status: 503 });
  }
}
