export const HSTS_HEADER_VALUE = "max-age=63072000; includeSubDomains";
export const CSP_NONCE_HEADER = "x-nonce";
export const API_DEFAULT_CSP = "default-src 'none'; frame-ancestors 'none'";

export const BASELINE_SECURITY_HEADERS: readonly { key: string; value: string }[] = [
  { key: "X-Content-Type-Options", value: "nosniff" },
  { key: "X-Frame-Options", value: "DENY" },
  { key: "Referrer-Policy", value: "strict-origin-when-cross-origin" },
  { key: "Permissions-Policy", value: "camera=(), microphone=(), geolocation=(), interest-cohort=()" },
];

export function isLoopbackHost(rawHost: string | null | undefined): boolean {
  if (!rawHost) return false;
  const first = rawHost.split(",")[0]?.trim().toLowerCase() ?? "";
  if (!first) return false;
  let hostname = first;
  if (first.startsWith("[")) {
    const end = first.indexOf("]");
    hostname = end === -1 ? first : first.slice(0, end + 1);
  } else {
    const colon = first.indexOf(":");
    if (colon !== -1 && first.indexOf(":", colon + 1) === -1) {
      hostname = first.slice(0, colon);
    }
  }
  return (
    hostname === "localhost" ||
    hostname.endsWith(".localhost") ||
    hostname === "127.0.0.1" ||
    hostname === "::1" ||
    hostname === "[::1]"
  );
}

export function shouldSetHsts(
  request: { url: string; headers: Headers },
  nodeEnv: string | undefined = process.env.NODE_ENV,
): boolean {
  if (nodeEnv !== "production") return false;
  const parsed = new URL(request.url);
  const host = request.headers.get("x-forwarded-host") || request.headers.get("host") || parsed.host;
  if (isLoopbackHost(host)) return false;
  const forwardedProto = request.headers.get("x-forwarded-proto")?.split(",")[0]?.trim().toLowerCase();
  if (forwardedProto) return forwardedProto === "https";
  return parsed.protocol === "https:";
}

export function generateCspNonce(): string {
  const bytes = new Uint8Array(16);
  crypto.getRandomValues(bytes);
  let binary = "";
  for (const byte of bytes) binary += String.fromCharCode(byte);
  return btoa(binary);
}

const SAFE_CSP_HOST_RE = /^[a-z0-9.\-:\[\]]+$/i;

function safeCspHosts(hosts: readonly (string | null | undefined)[] | undefined): string[] {
  if (!hosts) return [];
  const out: string[] = [];
  const seen = new Set<string>();
  for (const raw of hosts) {
    if (!raw) continue;
    const first = raw.split(",")[0]?.trim() ?? "";
    if (!first || !SAFE_CSP_HOST_RE.test(first)) continue;
    const lower = first.toLowerCase();
    if (seen.has(lower)) continue;
    seen.add(lower);
    out.push(first);
  }
  return out;
}

export function buildAppCsp(
  nonce: string,
  options?: { isDev?: boolean; host?: string | null; hosts?: readonly (string | null | undefined)[] },
): string {
  const scriptSources = ["'self'", `'nonce-${nonce}'`];
  if (options?.isDev) scriptSources.push("'unsafe-eval'");
  const connectSources = ["'self'"];
  const hosts = safeCspHosts([...(options?.hosts ?? []), options?.host]);
  for (const host of hosts) {
    connectSources.push(`wss://${host}`);
    if (options?.isDev || isLoopbackHost(host)) {
      connectSources.push(`ws://${host}`);
    }
  }
  return [
    "default-src 'self'",
    `script-src ${scriptSources.join(" ")}`,
    "style-src 'self' 'unsafe-inline'",
    "img-src 'self' data: blob:",
    "font-src 'self'",
    `connect-src ${connectSources.join(" ")}`,
    "frame-ancestors 'none'",
    "base-uri 'self'",
    "form-action 'self'",
    "object-src 'none'",
  ].join("; ");
}

export function applySecurityHeaders(
  response: Response,
  request: { url: string; headers: Headers },
  csp: string,
  nodeEnv?: string,
): void {
  for (const { key, value } of BASELINE_SECURITY_HEADERS) {
    response.headers.set(key, value);
  }
  if (!response.headers.has("Content-Security-Policy")) {
    response.headers.set("Content-Security-Policy", csp);
  }
  if (shouldSetHsts(request, nodeEnv)) {
    response.headers.set("Strict-Transport-Security", HSTS_HEADER_VALUE);
  }
}
