import assert from "node:assert/strict";
import http from "node:http";
import net from "node:net";
import tls from "node:tls";
import { describe, test } from "node:test";
import { Duplex } from "node:stream";
import { jwtVerify, SignJWT } from "jose";
import { testIdentityKeys } from "@/lib/runtime/test-keys";
import { asFetch } from "@/lib/test-fetch";
import { handleDesktopWSUpgrade, parseDesktopWSPath, resetWSProxyLogThrottle } from "./wsProxy";

describe("Desktop WebSocket upgrade proxy", () => {
  test("parseDesktopWSPath accepts valid session paths and rejects traversal or missing tokens", () => {
    assert.deepEqual(parseDesktopWSPath("/api/v1/computer/sessions/csess_12345/ws?token=abc_tok"), {
      sessionId: "csess_12345",
      token: "abc_tok",
    });
    assert.equal(parseDesktopWSPath("/api/v1/computer/sessions/csess_12345/ws"), null);
    assert.equal(parseDesktopWSPath("/api/v1/computer/sessions/../evil/ws?token=abc"), null);
    assert.equal(parseDesktopWSPath("/api/v1/computer/sessions/%zz/ws?token=abc"), null);
    assert.equal(parseDesktopWSPath("/api/v1/computer/sessions/csess_12345/ws?token=abc%0d%0aX-Injected:1"), null);
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

  test("resolves IAP and OIDC upgrades through shared auth and forwards only the service credential", async (t) => {
    const issuer = "https://oidc-ws.example.test";
    const audience = "nori-ws-test";
    const userID = "462cef1f-74a0-4706-94c7-3e624ff5cd43";
    const base = "https://runtime-ws.example.test/";
    const serviceKey = new TextEncoder().encode("service-key-for-websocket-tests-at-least-32-bytes");
    const settings = {
      NORI_CLOUD: "gcp", NORI_AUTH_PROVIDER: "oidc", NORI_OIDC_ISSUER: issuer, NORI_OIDC_AUDIENCE: audience,
      NORI_OIDC_COOKIE: "nori_id_token", NORI_APP_ORIGIN: "https://nori.example.test", NORI_RUNTIME_URL: base,
      NORI_SERVICE_TOKEN_KEY: new TextDecoder().decode(serviceKey), NORI_SERVICE_AUDIENCE: audience, IAP_AUDIENCE: audience,
      NORI_OIDC_IAP_POLICY_RESOURCE: undefined, NORI_OIDC_IAP_ALLOWED_IDENTITIES: undefined,
    };
    const previousEnv = Object.fromEntries(Object.keys(settings).map(key => [key, process.env[key]]));
    const previousFetch = globalThis.fetch;
    const previousConnect = tls.connect;
    t.after(() => {
      globalThis.fetch = previousFetch;
      tls.connect = previousConnect;
      for (const [key, value] of Object.entries(previousEnv)) {
        if (value === undefined) delete process.env[key];
        else process.env[key] = value;
      }
    });
    for (const [key, value] of Object.entries(settings)) {
      if (value === undefined) delete process.env[key];
      else process.env[key] = value;
    }
    const { privateKey, jwks } = await testIdentityKeys();
    const sign = (issuer: string, sub: string) => new SignJWT({ email: "user@gmail.com", email_verified: true })
      .setProtectedHeader({ alg: "ES256", kid: "auth-k1" }).setIssuer(issuer).setAudience(audience).setSubject(sub)
      .setIssuedAt().setExpirationTime("5m").sign(privateKey);
    const oidc = await sign(issuer, "oidc-user");
    const iap = await sign("https://cloud.google.com/iap", "accounts.google.com:iap-user");
    const resolutions: Request[] = [];
    globalThis.fetch = asFetch(async (input, init) => {
      const url = String(input);
      if (url === issuer + "/.well-known/openid-configuration") return Response.json({ issuer, jwks_uri: issuer + "/keys" });
      if (url === issuer + "/keys" || url === "https://www.gstatic.com/iap/verify/public_key-jwk") return Response.json(jwks);
      assert.equal(url, base + "api/users/resolve");
      resolutions.push(new Request(url, init));
      return Response.json({ id: userID });
    });
    const upstream: string[] = [];
    tls.connect = (() => {
      const transport = new Duplex({
        read() {},
        write(data, _encoding, done) {
          upstream.push(data.toString());
          transport.push("HTTP/1.1 101 Switching Protocols\r\n\r\n");
          transport.push(null);
          done();
        },
      });
      Object.assign(transport, { setTimeout() {} });
      queueMicrotask(() => transport.emit("secureConnect"));
      return transport;
    }) as unknown as typeof tls.connect;
    const server = http.createServer();
    server.on("upgrade", (req, socket, head) => {
      void handleDesktopWSUpgrade(req, socket, head).catch(error => socket.destroy(error));
    });
    await new Promise<void>(resolve => server.listen(0, "127.0.0.1", resolve));
    t.after(() => { server.close(); });
    const { port } = server.address() as net.AddressInfo;
    async function upgrade(headers: string[]) {
      return new Promise<string>((resolve, reject) => {
        const socket = net.connect(port, "127.0.0.1", () => socket.write([
          "GET /api/v1/computer/sessions/csess_12345/ws?token=desktop_token HTTP/1.1",
          "Host: nori.example.test", "Origin: https://nori.example.test",
          "Connection: Upgrade", "Upgrade: websocket", ...headers, "", "",
        ].join("\r\n")));
        socket.setTimeout(3000, () => socket.destroy(new Error("WebSocket response timed out")));
        let response = "";
        socket.on("data", chunk => { response += chunk.toString(); });
        socket.once("end", () => resolve(response));
        socket.once("error", reject);
      });
    }
    for (const headers of [[`Authorization: Bearer ${oidc}`], [`Cookie: nori_id_token=${oidc}`]]) {
      assert.match(await upgrade(headers), /^HTTP\/1\.1 101 /);
    }
    process.env.NORI_AUTH_PROVIDER = "iap";
    assert.match(await upgrade([`X-Goog-IAP-JWT-Assertion: ${iap}`]), /^HTTP\/1\.1 101 /);
    assert.match(await upgrade([]), /^HTTP\/1\.1 403 /);
    assert.equal(resolutions.length, 3);
    assert.deepEqual(resolutions.map(req => req.headers.get("x-nori-identity-subject")), ["oidc-user", "oidc-user", "iap-user"]);
    for (const req of resolutions) assert.equal((await req.json()).email, "user@gmail.com");
    for (const request of upstream) {
      assert.ok(request.startsWith(`GET /users/${userID}/v1/computer/sessions/csess_12345/ws HTTP/1.1\r\n`));
      assert.doesNotMatch(request, /\?token=/i);
      assert.match(request, /^X-Nori-Computer-Token: desktop_token\r?$/im);
      assert.doesNotMatch(request, /Cookie:|X-Goog-IAP-/i);
      const auth = /^Authorization: Bearer (.+)$/im.exec(request);
      assert.ok(auth);
      await jwtVerify(auth[1].trim(), serviceKey, { issuer: "nori-service", audience });
    }
    assert.equal(upstream.length, 3);
  });

  test("throttles repeated rejection warnings and propagates upstream WebSocket close frames", async (t) => {
    resetWSProxyLogThrottle();
    const warnings: string[] = [];
    const origWarn = console.warn;
    console.warn = (...args: unknown[]) => { warnings.push(args.map(String).join(" ")); };
    t.after(() => { console.warn = origWarn; });

    const server = http.createServer((_req, res) => { res.writeHead(404); res.end(); });
    server.on("upgrade", (req, socket, head) => { void handleDesktopWSUpgrade(req, socket, head); });
    await new Promise<void>((resolve) => server.listen(0, "127.0.0.1", resolve));
    t.after(() => { server.close(); });
    const { port } = server.address() as net.AddressInfo;

    const sendCrossOrigin = () => new Promise<string>((resolve, reject) => {
      const client = net.connect(port, "127.0.0.1", () => {
        client.write([
          "GET /api/v1/computer/sessions/csess_12345/ws?token=tok123 HTTP/1.1",
          `Host: 127.0.0.1:${port}`,
          "Origin: https://evil.example.test",
          "Upgrade: websocket",
          "Connection: Upgrade",
          "Sec-WebSocket-Key: dGhlIHNhbXBsZSBub25jZQ==",
          "Sec-WebSocket-Version: 13",
          "\r\n",
        ].join("\r\n"));
      });
      let data = "";
      client.on("data", (chunk) => { data += chunk.toString(); });
      client.on("end", () => resolve(data));
      client.on("error", reject);
    });

    for (let i = 0; i < 5; i++) {
      assert.match(await sendCrossOrigin(), /^HTTP\/1\.1 403 Forbidden/);
    }
    assert.equal(warnings.length, 1, "repeated cross-origin rejections must be throttled to one log per window");
    assert.match(warnings[0], /"stage":"cross_origin"/);

    // Verify graceful propagation of upstream WebSocket close frame (status code 4403 + reason).
    const issuer = "https://oidc-ws-close.example.test";
    const audience = "nori-ws-close-test";
    const userID = "99999999-9999-4999-8999-999999999999";
    const base = "https://runtime-ws-close.example.test/";
    const serviceKey = new TextEncoder().encode("service-key-for-ws-close-tests-at-least-32-bytes");
    const settings = {
      NORI_CLOUD: "gcp", NORI_AUTH_PROVIDER: "oidc", NORI_OIDC_ISSUER: issuer, NORI_OIDC_AUDIENCE: audience,
      NORI_OIDC_COOKIE: "nori_id_token", NORI_APP_ORIGIN: "https://nori.example.test", NORI_RUNTIME_URL: base,
      NORI_SERVICE_TOKEN_KEY: new TextDecoder().decode(serviceKey), NORI_SERVICE_AUDIENCE: audience,
      NORI_OIDC_IAP_POLICY_RESOURCE: undefined, NORI_OIDC_IAP_ALLOWED_IDENTITIES: undefined,
    };
    const previousEnv = Object.fromEntries(Object.keys(settings).map((k) => [k, process.env[k]]));
    const previousFetch = globalThis.fetch;
    const previousConnect = tls.connect;
    t.after(() => {
      globalThis.fetch = previousFetch;
      tls.connect = previousConnect;
      for (const [key, value] of Object.entries(previousEnv)) {
        if (value === undefined) delete process.env[key];
        else process.env[key] = value;
      }
    });
    for (const [key, value] of Object.entries(settings)) {
      if (value === undefined) delete process.env[key];
      else process.env[key] = value;
    }

    const { privateKey, jwks } = await testIdentityKeys();
    const token = await new SignJWT({ email: "close@gmail.com", email_verified: true })
      .setProtectedHeader({ alg: "ES256", kid: "auth-k1" }).setIssuer(issuer).setAudience(audience).setSubject("ws-close-user")
      .setIssuedAt().setExpirationTime("5m").sign(privateKey);

    globalThis.fetch = asFetch(async (input) => {
      const url = String(input);
      if (url === `${issuer}/.well-known/openid-configuration`) return Response.json({ issuer, jwks_uri: `${issuer}/keys` });
      if (url === `${issuer}/keys`) return Response.json(jwks);
      return Response.json({ id: userID });
    });

    // RFC 6455 unmasked server close frame: FIN=1, opcode=0x8, payload=[0x11, 0x33 (4403), ..."account_suspended"]
    const reasonBuf = Buffer.from("account_suspended", "utf8");
    const closePayload = Buffer.alloc(2 + reasonBuf.length);
    closePayload.writeUInt16BE(4403, 0);
    reasonBuf.copy(closePayload, 2);
    const closeFrame = Buffer.concat([Buffer.from([0x88, closePayload.length]), closePayload]);

    tls.connect = (() => {
      const transport = new Duplex({
        read() {},
        write(_data, _encoding, done) {
          transport.push(Buffer.from("HTTP/1.1 101 Switching Protocols\r\nUpgrade: websocket\r\nConnection: Upgrade\r\n\r\n"));
          transport.push(closeFrame);
          transport.push(null);
          done();
        },
      });
      Object.assign(transport, { setTimeout() {} });
      queueMicrotask(() => transport.emit("secureConnect"));
      return transport;
    }) as unknown as typeof tls.connect;

    const rawBytes = await new Promise<Buffer>((resolve, reject) => {
      const chunks: Buffer[] = [];
      const client = net.connect(port, "127.0.0.1", () => {
        client.write([
          "GET /api/v1/computer/sessions/csess_12345/ws?token=desktop_tok HTTP/1.1",
          "Host: nori.example.test",
          "Origin: https://nori.example.test",
          "Connection: Upgrade",
          "Upgrade: websocket",
          `Authorization: Bearer ${token}`,
          "", "",
        ].join("\r\n"));
      });
      client.on("data", (chunk: Buffer) => { chunks.push(chunk); });
      client.once("end", () => resolve(Buffer.concat(chunks)));
      client.once("error", reject);
    });

    const sep = rawBytes.indexOf("\r\n\r\n");
    assert.ok(sep > 0);
    assert.match(rawBytes.subarray(0, sep).toString("utf8"), /^HTTP\/1\.1 101 Switching Protocols/);
    const wsBytes = rawBytes.subarray(sep + 4);
    assert.equal(wsBytes[0], 0x88);
    assert.equal(wsBytes.readUInt16BE(2), 4403);
    assert.equal(wsBytes.subarray(4).toString("utf8"), "account_suspended");
  });
});
