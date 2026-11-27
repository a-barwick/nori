import type { IncomingMessage } from "node:http";
import net from "node:net";
import tls from "node:tls";
import type { Duplex } from "node:stream";
import { resolveUser, sameOrigin } from "@/lib/runtime/auth";
import { runtimeHeaders } from "@/lib/runtime/credentials";
import { runtimeMode, runtimeTarget } from "@/lib/runtime/target";

const SESSION_ID_RE = /^[A-Za-z0-9_-]{4,128}$/;

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
    if (!SESSION_ID_RE.test(sessionId) || !token || token.length > 512) return null;
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

function rejectSocket(socket: Duplex, statusCode: number, reason: string) {
  socket.end(`HTTP/1.1 ${statusCode} ${reason}\r\nConnection: close\r\nContent-Length: 0\r\n\r\n`, () => socket.destroy());
}

/**
 * Authenticated HTTP Upgrade tunnel from same-origin browser/Mac WebSocket
 * `/api/v1/computer/sessions/{id}/ws?token=...` to user-scoped `nori-core`
 * `/users/{userID}/v1/computer/sessions/{id}/ws?token=...` with Nori service auth.
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
    rejectSocket(socket, 403, "Forbidden");
    return true;
  }
  const mode = runtimeMode(headers);
  const target = runtimeTarget(mode);
  if (target instanceof Response) {
    rejectSocket(socket, target.status, "Service Unavailable");
    return true;
  }
  const user = await resolveUser(headers, target);
  if ("status" in user) {
    rejectSocket(socket, user.status, "Unauthorized");
    return true;
  }

  let authHeaders: Record<string, string>;
  try {
    authHeaders = await runtimeHeaders(mode);
  } catch {
    rejectSocket(socket, 503, "Service Unavailable");
    return true;
  }
  if (socket.destroyed) return true;

  const upstreamURL = new URL(
    `users/${encodeURIComponent(user.userID)}/v1/computer/sessions/${encodeURIComponent(parsed.sessionId)}/ws?token=${encodeURIComponent(parsed.token)}`,
    user.base,
  );
  const isTLS = upstreamURL.protocol === "https:";
  const port = Number(upstreamURL.port || (isTLS ? 443 : 80));
  const host = upstreamURL.hostname.replace(/^\[|\]$/g, "");
  const upstreamSocket = isTLS
    ? tls.connect({ host, port, servername: net.isIP(host) ? undefined : host })
    : net.connect({ host, port });

  let connected = false;
  upstreamSocket.setTimeout(15_000, () => upstreamSocket.destroy(new Error("Runtime connection timed out")));
  upstreamSocket.on("error", () => {
    if (connected) socket.destroy();
    else rejectSocket(socket, 502, "Bad Gateway");
  });
  socket.once("close", () => upstreamSocket.destroy());

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
    ];
    for (const [k, v] of Object.entries(authHeaders)) {
      lines.push(`${k}: ${v}`);
    }
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
