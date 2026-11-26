#!/usr/bin/env node
import { execFileSync } from "node:child_process";
import { mkdirSync, readFileSync, writeFileSync, existsSync } from "node:fs";
import https from "node:https";
import http from "node:http";
import { randomBytes } from "node:crypto";
import { join } from "node:path";
import { exportJWK, generateKeyPair, SignJWT } from "jose";

const [core = "http://127.0.0.1:8082", email = "frontend.real@example.com", alternateEmail] = process.argv.slice(2);
const target = new URL(core);
if (!["127.0.0.1", "localhost"].includes(target.hostname) || !email.endsWith("@example.com") || (alternateEmail && !alternateEmail.endsWith("@example.com"))) {
  throw new Error("prod-check only fronts a loopback core and only mints tokens for @example.com test addresses.");
}
const dir = process.env.NORI_PROD_CHECK_DIR || join(process.env.TMPDIR || "/tmp", "nori-prod-check");
mkdirSync(dir, { recursive: true, mode: 0o700 });
const file = (name) => join(dir, name);

if (!existsSync(file("cert.pem"))) {
  const run = (...args) => execFileSync("openssl", args, { stdio: "ignore" });
  run("req", "-x509", "-newkey", "rsa:2048", "-nodes", "-days", "7", "-subj", "/CN=nori prod-check CA", "-keyout", file("ca-key.pem"), "-out", file("ca.pem"));
  run("req", "-newkey", "rsa:2048", "-nodes", "-subj", "/CN=127.0.0.1", "-keyout", file("key.pem"), "-out", file("cert.csr"));
  writeFileSync(file("ext.cnf"), "subjectAltName=IP:127.0.0.1\nbasicConstraints=CA:FALSE\n");
  run("x509", "-req", "-in", file("cert.csr"), "-CA", file("ca.pem"), "-CAkey", file("ca-key.pem"), "-CAcreateserial", "-days", "7", "-extfile", file("ext.cnf"), "-out", file("cert.pem"));
}
const tls = { key: readFileSync(file("key.pem")), cert: readFileSync(file("cert.pem")) };

const ISSUER_PORT = 9443, RUNTIME_PORT = 9444;
const issuer = `https://127.0.0.1:${ISSUER_PORT}`;
const audience = "nori-prod-check";
const { publicKey, privateKey } = await generateKeyPair("ES256");
const jwk = { ...(await exportJWK(publicKey)), kid: "prod-check", alg: "ES256", use: "sig" };
const now = Math.floor(Date.now() / 1000);
const tokenFor = (email) => new SignJWT({ email, email_verified: true })
  .setProtectedHeader({ alg: "ES256", kid: "prod-check" })
  .setIssuer(issuer).setAudience(audience).setSubject(`prod-check:${email}`).setIssuedAt(now).setExpirationTime(now + 8 * 3600)
  .sign(privateKey);
const token = await tokenFor(email);
const alternateToken = alternateEmail ? await tokenFor(alternateEmail) : token;
writeFileSync(file("id-token"), token, { mode: 0o600 });
const serviceKey = randomBytes(32).toString("base64url");

https.createServer(tls, (req, res) => {
  const json = (value) => { res.writeHead(200, { "Content-Type": "application/json" }); res.end(JSON.stringify(value)); };
  if (req.url === "/.well-known/openid-configuration") return json({ issuer, jwks_uri: `${issuer}/jwks` });
  if (req.url === "/jwks") return json({ keys: [jwk] });
  res.writeHead(404); res.end();
}).listen(ISSUER_PORT, "127.0.0.1");

https.createServer(tls, (req, res) => {
  const upstream = http.request({ host: target.hostname, port: target.port, path: req.url, method: req.method, headers: { ...req.headers, host: target.host } }, (reply) => {
    res.writeHead(reply.statusCode ?? 502, reply.headers);
    reply.pipe(res);
  });
  upstream.on("error", () => { if (!res.headersSent) res.writeHead(502); res.end(); });
  req.pipe(upstream);
  res.on("close", () => upstream.destroy());
}).listen(RUNTIME_PORT, "127.0.0.1");

http.createServer((req, res) => {
  const url = new URL(req.url ?? "/", "http://127.0.0.1");
  const to = url.searchParams.get("to") ?? "";
  if (url.pathname !== "/sign-in" || !/^http:\/\/127\.0\.0\.1:\d+\//.test(to)) { res.writeHead(404); res.end(); return; }
  const signedIn = url.searchParams.get("account") === "second" ? alternateToken : token;
  res.writeHead(302, { "Set-Cookie": `nori_prod_check=${signedIn}; Path=/; HttpOnly; SameSite=Lax`, Location: to });
  res.end();
}).listen(9445, "127.0.0.1");

console.log(`prod-check: issuer ${issuer}, runtime https://127.0.0.1:${RUNTIME_PORT} -> ${core}, files in ${dir}`);
console.log("Start the production web with:");
console.log(`  NODE_EXTRA_CA_CERTS=${file("ca.pem")} NORI_CLOUD=aws NORI_AUTH_PROVIDER=oidc NORI_OIDC_ISSUER=${issuer} NORI_OIDC_AUDIENCE=${audience} NORI_OIDC_COOKIE=nori_prod_check NORI_RUNTIME_URL=https://127.0.0.1:${RUNTIME_PORT} NORI_SERVICE_TOKEN_KEY=<see ${file("service-key")}> NORI_APP_ORIGIN=http://127.0.0.1:<web port>  (writes otherwise require an https origin)`);
writeFileSync(file("service-key"), serviceKey, { mode: 0o600 });
console.log("Then open http://127.0.0.1:9445/sign-in?to=http://127.0.0.1:<web port>/ in the browser to sign in as the test address.");
