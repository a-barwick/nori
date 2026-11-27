// Production HTTP + WebSocket upgrade gateway for nori-web standalone container.
// Proxies ordinary HTTP requests to Next.js standalone server.js on an internal
// loopback port and tunnels authenticated same-origin WebSocket upgrades for
// /api/v1/computer/sessions/{id}/ws directly to NORI_RUNTIME_URL with Nori service auth.

import http from "node:http";
import { spawn } from "node:child_process";
import { handleDesktopWSUpgrade } from "./src/lib/computer/wsProxy";

const PUBLIC_PORT = Number(process.env.PORT || 8080);
const PUBLIC_HOST = process.env.HOSTNAME || "0.0.0.0";
const NEXT_PORT = PUBLIC_PORT === 8081 ? 8082 : PUBLIC_PORT + 1;

// Launch Next.js standalone server on internal loopback port.
const nextProc = spawn(process.execPath, ["server.js"], {
  env: { ...process.env, HOSTNAME: "127.0.0.1", PORT: String(NEXT_PORT) },
  stdio: "inherit",
});
nextProc.on("exit", (code) => process.exit(code ?? 1));
for (const signal of ["SIGINT", "SIGTERM"]) {
  process.on(signal, () => nextProc.kill(signal));
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
    socket.end("HTTP/1.1 404 Not Found\r\nConnection: close\r\nContent-Length: 0\r\n\r\n");
  } catch {
    socket.destroy();
  }
});

server.listen(PUBLIC_PORT, PUBLIC_HOST);
