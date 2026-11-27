// HTTP + WebSocket upgrade gateway for nori-web (production standalone and --dev mode).
// Proxies ordinary HTTP requests to Next.js on an internal loopback port and tunnels
// authenticated same-origin WebSocket upgrades for /api/v1/computer/sessions/{id}/ws
// directly to NORI_RUNTIME_URL / NORI_LOCAL_AGENT_URL with Nori service auth.

import http from "node:http";
import net from "node:net";
import { spawn } from "node:child_process";
import { handleDesktopWSUpgrade } from "./src/lib/computer/wsProxy";

const rawArgs = process.argv.slice(2);
const isDev = rawArgs.includes("--dev");
let cliPort;
let cliHost;
const nextExtraArgs = [];
for (let i = 0; i < rawArgs.length; i++) {
  const arg = rawArgs[i];
  if (arg === "--dev") continue;
  if ((arg === "--port" || arg === "-p") && i + 1 < rawArgs.length) {
    cliPort = Number(rawArgs[++i]);
    continue;
  }
  if ((arg === "--hostname" || arg === "-H") && i + 1 < rawArgs.length) {
    cliHost = rawArgs[++i];
    continue;
  }
  nextExtraArgs.push(arg);
}

const PUBLIC_PORT = cliPort || Number(process.env.PORT || (isDev ? 3210 : 8080));
const PUBLIC_HOST = cliHost || process.env.HOSTNAME || (isDev ? "127.0.0.1" : "0.0.0.0");
const NEXT_PORT = PUBLIC_PORT === 8081 ? 8082 : PUBLIC_PORT + 1;

// Launch Next.js server on internal loopback port.
const nextProc = isDev
  ? spawn("./node_modules/.bin/next", ["dev", "-H", "127.0.0.1", "-p", String(NEXT_PORT), ...nextExtraArgs], {
      env: { ...process.env, HOSTNAME: "127.0.0.1", PORT: String(NEXT_PORT) },
      stdio: "inherit",
    })
  : spawn(process.execPath, ["server.js"], {
      env: { ...process.env, HOSTNAME: "127.0.0.1", PORT: String(NEXT_PORT) },
      stdio: "inherit",
    });
nextProc.on("exit", (code) => process.exit(code ?? 1));
for (const signal of ["SIGINT", "SIGTERM"]) {
  process.on(signal, () => nextProc.kill(signal));
}

function proxyUpgradeToNext(req, socket, head) {
  const upstream = net.connect(NEXT_PORT, "127.0.0.1", () => {
    const headerLines = [`${req.method || "GET"} ${req.url || "/"} HTTP/1.1`];
    for (let i = 0; i < req.rawHeaders.length; i += 2) {
      headerLines.push(`${req.rawHeaders[i]}: ${req.rawHeaders[i + 1]}`);
    }
    upstream.write(headerLines.join("\r\n") + "\r\n\r\n");
    if (head && head.length > 0) upstream.write(head);
    upstream.pipe(socket);
    socket.pipe(upstream);
  });
  upstream.on("error", () => socket.destroy());
  socket.once("close", () => upstream.destroy());
}

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
  res.on("close", () => {
    if (!res.writableEnded) proxyReq.destroy();
  });
  proxyReq.on("error", () => {
    if (!res.headersSent) {
      res.writeHead(502, { "Content-Type": "application/json" });
      res.end(JSON.stringify({ error: "Frontend server unavailable." }));
    }
  });
  req.pipe(proxyReq);
});

server.on("upgrade", async (req, socket, head) => {
  socket.on("error", () => socket.destroy());
  try {
    if (await handleDesktopWSUpgrade(req, socket, head)) return;
    if (isDev) {
      proxyUpgradeToNext(req, socket, head);
      return;
    }
    socket.end("HTTP/1.1 404 Not Found\r\nConnection: close\r\nContent-Length: 0\r\n\r\n");
  } catch (err) {
    console.error("[ws-server] upgrade failed:", err instanceof Error ? err.message : String(err));
    socket.destroy();
  }
});

server.listen(PUBLIC_PORT, PUBLIC_HOST);
