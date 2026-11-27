import assert from "node:assert/strict";
import http from "node:http";
import net from "node:net";
import { describe, test } from "node:test";
import { handleDesktopWSUpgrade, parseDesktopWSPath } from "./wsProxy";

describe("Desktop WebSocket upgrade proxy", () => {
  test("parseDesktopWSPath accepts valid session paths and rejects traversal or missing tokens", () => {
    assert.deepEqual(parseDesktopWSPath("/api/v1/computer/sessions/csess_12345/ws?token=abc_tok"), {
      sessionId: "csess_12345",
      token: "abc_tok",
    });
    assert.equal(parseDesktopWSPath("/api/v1/computer/sessions/csess_12345/ws"), null);
    assert.equal(parseDesktopWSPath("/api/v1/computer/sessions/../evil/ws?token=abc"), null);
    assert.equal(parseDesktopWSPath("/api/v1/work"), null);
  });

  test("rejects cross-origin WebSocket upgrades with 403", async () => {
    const server = http.createServer((_req, res) => {
      res.writeHead(404);
      res.end();
    });
    server.on("upgrade", (req, socket, head) => {
      void handleDesktopWSUpgrade(req, socket, head);
    });
    await new Promise<void>((resolve) => server.listen(0, "127.0.0.1", resolve));
    const addr = server.address() as net.AddressInfo;

    try {
      const response = await new Promise<string>((resolve, reject) => {
        const client = net.connect(addr.port, "127.0.0.1", () => {
          client.write(
            [
              `GET /api/v1/computer/sessions/csess_12345/ws?token=tok123 HTTP/1.1`,
              `Host: 127.0.0.1:${addr.port}`,
              "Origin: https://evil.example.test",
              "Upgrade: websocket",
              "Connection: Upgrade",
              "Sec-WebSocket-Key: dGhlIHNhbXBsZSBub25jZQ==",
              "Sec-WebSocket-Version: 13",
              "\r\n",
            ].join("\r\n"),
          );
        });
        let data = "";
        client.on("data", (chunk) => { data += chunk.toString(); });
        client.on("end", () => resolve(data));
        client.on("error", reject);
      });
      assert.match(response, /^HTTP\/1\.1 403 Forbidden/);
    } finally {
      server.close();
    }
  });
});
