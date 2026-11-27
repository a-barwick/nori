// Production HTTP + WebSocket upgrade gateway for nori-web standalone container.
// Proxies ordinary HTTP requests to Next.js standalone server.js on an internal
// loopback port and tunnels authenticated same-origin WebSocket upgrades for
// /api/v1/computer/sessions/{id}/ws directly to NORI_RUNTIME_URL with Nori service auth.

import http from "node:http";
import net from "node:net";
import tls from "node:tls";
import { spawn } from "node:child_process";
import { SignJWT } from "jose";

const PUBLIC_PORT = Number(process.env.PORT || 8080);
const PUBLIC_HOST = process.env.HOSTNAME || "0.0.0.0";
const NEXT_PORT = PUBLIC_PORT === 8081 ? 8082 : PUBLIC_PORT + 1;
const SESSION_ID_RE = /^[A-Za-z0-9_-]{4,128}$/;

function parseDesktopWS(rawURL) {
  if (!rawURL) return null;
  let parsed;
  try {
    parsed = new URL(rawURL, "http://localhost");
  } catch {
    return null;
  }
  const m = /^\/api\/v1\/computer\/sessions\/([^/]+)\/ws$/.exec(parsed.pathname);
  if (!m) return null;
  const sessionId = decodeURIComponent(m[1]);
  const token = (parsed.searchParams.get("token") || "").trim();
  if (!SESSION_ID_RE.test(sessionId) || !token || token.length > 512) return null;
  return { sessionId, token };
}

function isSameOrigin(req) {
  const origin = req.headers.origin;
  const host = req.headers["x-forwarded-host"] || req.headers.host;
  if (!origin || !host) return false;
  try {
    return new URL(origin).host.toLowerCase() === String(host).split(",")[0].trim().toLowerCase();
  } catch {
    return false;
  }
}

async function serviceAuthHeader() {
  const key = new TextEncoder().encode(process.env.NORI_SERVICE_TOKEN_KEY || "");
  const audience = process.env.NORI_SERVICE_AUDIENCE || "nori";
  if (key.length < 32 || !audience.trim()) {
    throw new Error("Service authentication not configured");
  }
  const jwt = await new SignJWT({})
    .setProtectedHeader({ alg: "HS256", typ: "JWT" })
    .setIssuer("nori-service")
    .setAudience(audience)
    .setIssuedAt()
    .setExpirationTime("60s")
    .sign(key);
  return `Bearer ${jwt}`;
}

function rejectSocket(socket, code, reason) {
  socket.write(`HTTP/1.1 ${code} ${reason}\r\nConnection: close\r\nContent-Length: 0\r\n\r\n`);
  socket.destroy();
}

// Launch Next.js standalone server on internal loopback port.
const nextProc = spawn(process.execPath, ["server.js"], {
  env: { ...process.env, HOSTNAME: "127.0.0.1", PORT: String(NEXT_PORT) },
  stdio: "inherit",
});
nextProc.on("exit", (code) => process.exit(code ?? 1));

const server = http.createServer((req, res) => {
  const proxyReq = http.request(
    {
      hostname: "127.0.0.1",
      port: NEXT_PORT,
      path: req.url,
      method: req.method,
      headers: req.headers,
    },
    (proxyRes) => {
      res.writeHead(proxyRes.statusCode || 502, proxyRes.headers);
      proxyRes.pipe(res);
    },
  );
  proxyReq.on("error", () => {
    if (!res.headersSent) {
      res.writeHead(502, { "Content-Type": "application/json" });
      res.end(JSON.stringify({ error: "Frontend server unavailable." }));
    }
  });
  req.pipe(proxyReq);
});

server.on("upgrade", async (req, socket, head) => {
  const parsed = parseDesktopWS(req.url);
  if (!parsed) {
    rejectSocket(socket, 404, "Not Found");
    return;
  }
  if (!isSameOrigin(req)) {
    rejectSocket(socket, 403, "Forbidden");
    return;
  }
  const runtimeRaw = process.env.NORI_RUNTIME_URL || process.env.NORI_LOCAL_AGENT_URL;
  if (!runtimeRaw) {
    rejectSocket(socket, 503, "Service Unavailable");
    return;
  }

  // Authenticate the caller via Next.js's own same-origin auth middleware (/api/v1/computer)
  // so IAP / OIDC / Mac identity checks apply identically to WebSocket upgrades.
  let userId = "";
  try {
    const checkHeaders = { ...req.headers };
    delete checkHeaders.upgrade;
    delete checkHeaders.connection;
    const statusRes = await fetch(`http://127.0.0.1:${NEXT_PORT}/api/v1/computer`, {
      method: "GET",
      headers: checkHeaders,
    });
    if (!statusRes.ok) {
      rejectSocket(socket, statusRes.status, "Unauthorized");
      return;
    }
    const statusJson = await statusRes.json().catch(() => null);
    if (!statusJson || !statusJson.enabled) {
      rejectSocket(socket, 503, "Service Unavailable");
      return;
    }
    const resolveAuth = process.env.NORI_RUNTIME_URL ? await serviceAuthHeader() : undefined;
    // Resolve user ID via nori-core using the verified email header (IAP) or OIDC bearer/cookie.
    const runtimeBase = new URL(runtimeRaw.endsWith("/") ? runtimeRaw : `${runtimeRaw}/`);
    const iapEmail = String(req.headers["x-goog-authenticated-user-email"] || "").replace(/^accounts\.google\.com:/i, "");
    const resHeaders = { "Content-Type": "application/json" };
    if (resolveAuth) resHeaders.Authorization = resolveAuth;
    if (req.headers["x-nori-identity-issuer"]) resHeaders["X-Nori-Identity-Issuer"] = String(req.headers["x-nori-identity-issuer"]);
    if (req.headers["x-nori-identity-subject"]) resHeaders["X-Nori-Identity-Subject"] = String(req.headers["x-nori-identity-subject"]);
    const userRes = await fetch(new URL("api/users/resolve", runtimeBase), {
      method: "POST",
      headers: resHeaders,
      body: JSON.stringify(iapEmail ? { email: iapEmail } : {}),
    });
    if (!userRes.ok) {
      rejectSocket(socket, 403, "Forbidden");
      return;
    }
    const resolved = await userRes.json();
    userId = resolved.id || "";
    if (!userId) {
      rejectSocket(socket, 403, "Forbidden");
      return;
    }

    const upstreamURL = new URL(
      `users/${encodeURIComponent(userId)}/v1/computer/sessions/${encodeURIComponent(parsed.sessionId)}/ws?token=${encodeURIComponent(parsed.token)}`,
      runtimeBase,
    );
    const isTLS = upstreamURL.protocol === "https:";
    const port = Number(upstreamURL.port || (isTLS ? 443 : 80));
    const upstream = isTLS
      ? tls.connect({ host: upstreamURL.hostname, port, servername: upstreamURL.hostname })
      : net.connect({ host: upstreamURL.hostname, port });

    upstream.once("error", () => rejectSocket(socket, 502, "Bad Gateway"));
    socket.once("error", () => upstream.destroy());

    const onReady = () => {
      const lines = [
        `GET ${upstreamURL.pathname}${upstreamURL.search} HTTP/1.1`,
        `Host: ${upstreamURL.host}`,
        "Upgrade: websocket",
        "Connection: Upgrade",
        `Sec-WebSocket-Key: ${req.headers["sec-websocket-key"] || ""}`,
        `Sec-WebSocket-Version: ${req.headers["sec-websocket-version"] || "13"}`,
        `Sec-WebSocket-Protocol: ${req.headers["sec-websocket-protocol"] || "binary"}`,
        "X-Nori-Client: web",
      ];
      if (resolveAuth) lines.push(`Authorization: ${resolveAuth}`);
      upstream.write(lines.join("\r\n") + "\r\n\r\n");
      if (head && head.length > 0) upstream.write(head);
      upstream.pipe(socket);
      socket.pipe(upstream);
    };
    upstream.once(isTLS ? "secureConnect" : "connect", onReady);
  } catch {
    rejectSocket(socket, 502, "Bad Gateway");
  }
});

server.listen(PUBLIC_PORT, PUBLIC_HOST);
