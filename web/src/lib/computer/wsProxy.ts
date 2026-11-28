import type { IncomingMessage } from "node:http";
import net from "node:net";
import tls from "node:tls";
import type { Duplex } from "node:stream";
import { evictResolvedUser, resolveUser, sameOrigin } from "@/lib/runtime/auth";
import { runtimeHeaders } from "@/lib/runtime/credentials";
import { runtimeMode, runtimeTarget } from "@/lib/runtime/target";

const SESSION_ID_RE = /^[A-Za-z0-9_-]{4,128}$/;
const REJECT_LOG_WINDOW_MS = 60_000;
const PEER_CLOSE_DESTROY_TIMEOUT_MS = 1_000;

const STATUS_REASONS: Record<number, string> = {
  400: "Bad Request",
  401: "Unauthorized",
  402: "Payment Required",
  403: "Forbidden",
  429: "Too Many Requests",
  502: "Bad Gateway",
  503: "Service Unavailable",
};

const rejectLogThrottle = new Map<string, { lastLoggedAt: number; suppressed: number }>();

export function resetWSProxyLogThrottle() {
  rejectLogThrottle.clear();
}

function logThrottledRejection(
  stage: string,
  fields: Record<string, unknown>,
  severity: "WARNING" | "ERROR" = "WARNING",
) {
  const now = Date.now();
  if (rejectLogThrottle.size > 100) rejectLogThrottle.clear();
  const entry = rejectLogThrottle.get(stage);
  if (entry && now - entry.lastLoggedAt < REJECT_LOG_WINDOW_MS) {
    entry.suppressed++;
    return;
  }
  const suppressed = entry?.suppressed ?? 0;
  rejectLogThrottle.set(stage, { lastLoggedAt: now, suppressed: 0 });
  const payload = JSON.stringify({
    severity,
    message: "nori.computer_ws.rejected",
    stage,
    ...(suppressed > 0 ? { suppressed } : {}),
    ...fields,
  });
  if (severity === "ERROR") console.error(payload);
  else console.warn(payload);
}

export interface ParsedDesktopWS {
  sessionId: string;
  token: string;
}

export function parseDesktopWSPath(rawURL: string | undefined): ParsedDesktopWS | null {
  if (!rawURL) return null;
  try {
    const parsed = new URL(rawURL, "http://localhost");
    const match = /^\/api\/v1\/computer\/sessions\/([^/]+)\/ws$/.exec(parsed.pathname);
    if (!match) return null;
    const sessionId = decodeURIComponent(match[1]);
    const token = (parsed.searchParams.get("token") ?? "").trim();
    if (!SESSION_ID_RE.test(sessionId) || !token || token.length > 512 || /[\x00-\x1f\x7f]/.test(token)) return null;
    return { sessionId, token };
  } catch {
    return null;
  }
}

function incomingToHeaders(rawHeaders: IncomingMessage["headers"]): Headers {
  const out = new Headers();
  for (const [k, v] of Object.entries(rawHeaders)) {
    if (Array.isArray(v)) {
      for (const item of v) out.append(k, item);
    } else if (typeof v === "string") {
      out.set(k, v);
    }
  }
  return out;
}

function rejectSocket(
  socket: Duplex,
  statusCode: number,
  reason?: string,
  extraHeaders?: Record<string, string>,
) {
  const phrase = reason || STATUS_REASONS[statusCode] || "Error";
  const lines = [`HTTP/1.1 ${statusCode} ${phrase}`, "Connection: close", "Content-Length: 0"];
  if (extraHeaders) {
    for (const [key, value] of Object.entries(extraHeaders)) {
      if (value && !/[\r\n]/.test(value)) lines.push(`${key}: ${value}`);
    }
  }
  socket.end(lines.join("\r\n") + "\r\n\r\n", () => socket.destroy());
}

/**
 * Authenticated HTTP Upgrade tunnel from same-origin browser/Mac WebSocket
 * `/api/v1/computer/sessions/{id}/ws?token=...` to user-scoped `nori-core`
 * `/users/{userID}/v1/computer/sessions/{id}/ws` with Nori service auth and
 * `X-Nori-Computer-Token`.
 */
export async function handleDesktopWSUpgrade(
  req: IncomingMessage,
  socket: Duplex,
  head: Buffer,
): Promise<boolean> {
  const parsed = parseDesktopWSPath(req.url);
  if (!parsed) {
    return false;
  }
  socket.pause();
  const headers = incomingToHeaders(req.headers);
  if (!sameOrigin({ headers })) {
    logThrottledRejection("cross_origin", {
      sessionId: parsed.sessionId,
      origin: headers.get("origin") ?? "",
      host: headers.get("x-forwarded-host") ?? headers.get("host") ?? "",
    });
    rejectSocket(socket, 403, "Forbidden");
    return true;
  }
  const mode = runtimeMode(headers);
  const target = runtimeTarget(mode);
  if (target instanceof Response) {
    logThrottledRejection("target_unavailable", { sessionId: parsed.sessionId, status: target.status });
    rejectSocket(socket, target.status, STATUS_REASONS[target.status] ?? "Service Unavailable");
    return true;
  }
  const user = await resolveUser(headers, target);
  if ("status" in user) {
    logThrottledRejection("auth_failed", {
      sessionId: parsed.sessionId,
      status: user.status,
      code: user.code ?? "",
    });
    rejectSocket(socket, user.status, STATUS_REASONS[user.status] ?? "Forbidden", {
      ...(user.code ? { "X-Nori-Error-Code": user.code } : {}),
      ...(user.retryAfter ? { "Retry-After": user.retryAfter } : {}),
    });
    return true;
  }

  let authHeaders: Record<string, string>;
  try {
    authHeaders = await runtimeHeaders(mode);
  } catch (err) {
    logThrottledRejection(
      "service_auth_failed",
      {
        sessionId: parsed.sessionId,
        error: err instanceof Error ? err.message : String(err),
      },
      "ERROR",
    );
    rejectSocket(socket, 503, "Service Unavailable");
    return true;
  }
  if (socket.destroyed) return true;

  const upstreamURL = new URL(
    `users/${encodeURIComponent(user.userID)}/v1/computer/sessions/${encodeURIComponent(parsed.sessionId)}/ws`,
    user.base,
  );
  const isTLS = upstreamURL.protocol === "https:";
  const port = Number(upstreamURL.port || (isTLS ? 443 : 80));
  const host = upstreamURL.hostname.replace(/^\[|\]$/g, "");
  const upstreamSocket = isTLS
    ? tls.connect({ host, port, servername: net.isIP(host) ? undefined : host })
    : net.connect({ host, port });

  let connected = false;
  let inspectedHandshake = false;
  upstreamSocket.setTimeout(15_000, () => upstreamSocket.destroy(new Error("Runtime connection timed out")));
  upstreamSocket.on("error", (err) => {
    logThrottledRejection("upstream_error", {
      sessionId: parsed.sessionId,
      connected,
      error: err instanceof Error ? err.message : String(err),
    });
    if (connected) socket.destroy();
    else rejectSocket(socket, 502, "Bad Gateway");
  });
  socket.on("error", () => {
    if (!upstreamSocket.destroyed) upstreamSocket.destroy();
  });
  // Propagate graceful session closes (including WebSocket close frames with
  // close codes/reasons) before tearing down the peer socket, and enforce a
  // bounded destroy so a half-open socket cannot linger.
  const peerTimers = new WeakMap<Duplex, ReturnType<typeof setTimeout>>();
  const armPeerDestroy = (peer: Duplex) => {
    if (peer.destroyed || peerTimers.has(peer)) return;
    const timer = setTimeout(() => {
      peerTimers.delete(peer);
      if (!peer.destroyed) peer.destroy();
    }, PEER_CLOSE_DESTROY_TIMEOUT_MS);
    timer.unref?.();
    peerTimers.set(peer, timer);
    peer.once("close", () => {
      clearTimeout(timer);
      peerTimers.delete(peer);
    });
  };
  const closePeerGracefully = (peer: Duplex, hadError: boolean) => {
    if (peer.destroyed) return;
    if (hadError) {
      const existing = peerTimers.get(peer);
      if (existing) {
        clearTimeout(existing);
        peerTimers.delete(peer);
      }
      peer.destroy();
      return;
    }
    armPeerDestroy(peer);
    peer.end();
  };
  upstreamSocket.once("end", () => {
    armPeerDestroy(socket);
  });
  upstreamSocket.once("close", (hadError) => {
    closePeerGracefully(socket, hadError);
  });
  socket.once("end", () => {
    closePeerGracefully(upstreamSocket, false);
  });
  socket.once("close", (hadError: boolean) => {
    closePeerGracefully(upstreamSocket, hadError);
  });

  const onConnect = () => {
    connected = true;
    upstreamSocket.setTimeout(0);
    const wsKey = headers.get("sec-websocket-key") ?? "";
    const wsVersion = headers.get("sec-websocket-version") ?? "13";
    const wsProto = headers.get("sec-websocket-protocol") ?? "binary";
    const lines = [
      `GET ${upstreamURL.pathname}${upstreamURL.search} HTTP/1.1`,
      `Host: ${upstreamURL.host}`,
      "Upgrade: websocket",
      "Connection: Upgrade",
      `Sec-WebSocket-Key: ${wsKey}`,
      `Sec-WebSocket-Version: ${wsVersion}`,
      `Sec-WebSocket-Protocol: ${wsProto}`,
      "X-Nori-Client: web",
      `X-Nori-Computer-Token: ${parsed.token}`,
    ];
    for (const [k, v] of Object.entries(authHeaders)) {
      lines.push(`${k}: ${v}`);
    }
    upstreamSocket.on("data", (chunk: Buffer | string) => {
      if (inspectedHandshake) return;
      inspectedHandshake = true;
      const preview = typeof chunk === "string" ? chunk : chunk.subarray(0, 1024).toString("utf8");
      if (/^HTTP\/1\.[01] 403\b/.test(preview) && preview.includes("account_suspended")) {
        evictResolvedUser(user.userID);
      }
    });
    upstreamSocket.write(lines.join("\r\n") + "\r\n\r\n");
    if (head && head.length > 0) {
      upstreamSocket.write(head);
    }
    upstreamSocket.pipe(socket);
    socket.pipe(upstreamSocket);
  };

  if (isTLS) {
    upstreamSocket.once("secureConnect", onConnect);
  } else {
    upstreamSocket.once("connect", onConnect);
  }
  return true;
}
