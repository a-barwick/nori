"use strict";

const assert = require("node:assert/strict");
const test = require("node:test");
const { createHash } = require("node:crypto");
const { receiveCode } = require("../google-auth.cjs");
const { canCallDesktop, isSameOrigin, readConfig } = require("../policy.cjs");

const webOrigin = "https://nori.example";

test("Home AI IPC checks the full origin and Models route", () => {
  assert.equal(canCallDesktop(webOrigin + "/settings/models", webOrigin, true), true);
  assert.equal(canCallDesktop(webOrigin + "/settings/models?section=home", webOrigin, true), true);
  for (const url of [
    "https://nori.example.attacker.example/settings/models",
    "https://nori.example:444/settings/models",
    "https://nori.example@attacker.example/settings/models",
    "https://user@nori.example/settings/models",
    "http://nori.example/settings/models",
    "blob:https://nori.example/settings/models",
    webOrigin + "/settings",
  ]) {
    assert.equal(canCallDesktop(url, webOrigin, true), false, url);
  }
  assert.equal(canCallDesktop(webOrigin + "/settings", webOrigin, false), true);
  assert.equal(isSameOrigin("not a URL", webOrigin), false);
});

test("A loopback test build cannot send a production Google credential", () => {
  const local = { webOrigin: "http://127.0.0.1:3210", relayOrigin: "http://127.0.0.1:8092" };
  assert.equal(readConfig(local).local, true);
  assert.throws(() => readConfig({ ...local, googleClientId: "test.apps.googleusercontent.com" }));
  assert.throws(() => readConfig({ ...local, relayOrigin: "https://relay.example" }));
  assert.throws(() => readConfig({ ...local, webOrigin: "http://192.168.1.4:3210" }));
});

test("The browser callback is one-use and arrives on loopback with the original state and PKCE challenge", async () => {
  let challenge;
  let callbackURL;
  const result = await receiveCode({ googleClientId: "desktop.apps.googleusercontent.com" }, async (url) => {
    const auth = new URL(url);
    assert.equal(auth.origin, "https://accounts.google.com");
    assert.equal(auth.searchParams.get("code_challenge_method"), "S256");
    challenge = auth.searchParams.get("code_challenge");
    callbackURL = new URL(auth.searchParams.get("redirect_uri"));
    assert.equal(callbackURL.hostname, "127.0.0.1");
    callbackURL.search = new URLSearchParams({ code: "wrong", state: "wrong" });
    const rejected = await fetch(callbackURL);
    assert.equal(rejected.status, 400);
    callbackURL.search = new URLSearchParams({ code: "correct", state: auth.searchParams.get("state") });
    const approved = await fetch(callbackURL);
    assert.equal(approved.status, 200);
    assert.equal(approved.headers.get("cache-control"), "no-store");
    assert.equal((await fetch(callbackURL)).status, 410);
  }, new AbortController().signal);

  assert.equal(result.code, "correct");
  assert.equal(createHash("sha256").update(result.verifier).digest("base64url"), challenge);
  await assert.rejects(fetch(callbackURL));
});
